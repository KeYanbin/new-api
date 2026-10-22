package helper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
)

const (
	InitialScannerBufferSize    = 64 << 10  // 64KB (64*1024)
	DefaultMaxScannerBufferSize = 128 << 20 // 64MB (64*1024*1024) default SSE buffer size
	DefaultPingInterval         = 10 * time.Second
	// streamWriteTimeout bounds a single blocked write to a slow client so the
	// unconditional wg.Wait() in cleanup can always finish. Without it, a slow
	// but connected client (full TCP buffer, no server WriteTimeout) could hang
	// the handler forever.
	streamWriteTimeout = 30 * time.Second
)

func getScannerBufferSize() int {
	if constant.StreamScannerMaxBufferMB > 0 {
		return constant.StreamScannerMaxBufferMB << 20
	}
	return DefaultMaxScannerBufferSize
}

// NewStreamScanner shares relay scanner configuration. Callers buffering bounded
// task state may additionally cap a line without increasing the configured limit.
func NewStreamScanner(reader io.Reader, maxBytes ...int) *bufio.Scanner {
	limit := getScannerBufferSize()
	if len(maxBytes) > 0 && maxBytes[0] > 0 {
		limit = min(limit, maxBytes[0])
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, min(InitialScannerBufferSize, limit)), limit)
	return scanner
}

func copyCodexSSEHeaders(c *gin.Context, resp *http.Response) {
	if c == nil || c.Writer == nil || resp == nil {
		return
	}
	// codex
	for _, name := range []string{"X-Reasoning-Included", "X-Codex-Turn-State"} {
		values := resp.Header.Values(name)
		if !service.ShouldCopyUpstreamHeader(c, name, values) {
			continue
		}
		for _, value := range values {
			if value != "" {
				c.Writer.Header().Add(name, value)
			}
		}
	}
}

// ExtendWriteDeadline pushes the connection write deadline forward before each
// stream write. Best-effort: writers that don't support deadlines (e.g.
// httptest recorders) are silently ignored.
func ExtendWriteDeadline(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
}

type StreamScannerOptions struct {
	// ClientDisconnectTimeout（客户端断开后读取用量的最长等待时间）为零时立即关闭上游。
	ClientDisconnectTimeout time.Duration
}

func StreamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult), options ...StreamScannerOptions) {

	if resp == nil || dataHandler == nil {
		return
	}

	// Finish the request keepalive before changing headers. The scanner owns
	// subsequent ping and data writes through its shared write mutex.
	if writer, ok := c.Writer.(*streamKeepAliveWriter); ok {
		writer.stop()
	}

	// 无条件新建 StreamStatus
	info.StreamStatus = relaycommon.NewStreamStatus()

	ctx, cancel := context.WithCancel(context.Background())

	streamingTimeout := time.Duration(constant.StreamingTimeout) * time.Second
	var disconnectTimeout time.Duration
	if len(options) > 0 {
		disconnectTimeout = max(0, options[0].ClientDisconnectTimeout)
	}

	var (
		stopChan       = make(chan bool, 3) // 增加缓冲区避免阻塞
		scanner        = NewStreamScanner(resp.Body)
		ticker         = time.NewTicker(streamingTimeout)
		pingTicker     *time.Ticker
		writeMutex     sync.Mutex     // Mutex to protect concurrent writes
		wg             sync.WaitGroup // 用于等待所有 goroutine 退出
		cleanupOnce    sync.Once
		stopOnce       sync.Once
		clientGone     = make(chan struct{})
		disconnectOnce sync.Once
	)
	disconnect := func(err error) {
		disconnectOnce.Do(func() {
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, err)
			close(clientGone)
		})
	}
	if err := c.Request.Context().Err(); err != nil {
		disconnect(err)
	}

	stop := func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
	}

	generalSettings := operation_setting.GetGeneralSetting()
	pingEnabled := generalSettings.PingIntervalEnabled && !info.DisablePing
	pingInterval := time.Duration(generalSettings.PingIntervalSeconds) * time.Second
	if pingInterval <= 0 {
		pingInterval = DefaultPingInterval
	}

	if pingEnabled {
		pingTicker = time.NewTicker(pingInterval)
	}

	logger.LogDebug(c, "relay timeout seconds: %d", common.RelayTimeout)
	logger.LogDebug(c, "relay max idle conns: %d", common.RelayMaxIdleConns)
	logger.LogDebug(c, "relay max idle conns per host: %d", common.RelayMaxIdleConnsPerHost)
	logger.LogDebug(c, "streaming timeout seconds: %d", int64(streamingTimeout.Seconds()))
	logger.LogDebug(c, "ping interval seconds: %d", int64(pingInterval.Seconds()))

	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			stop()
			if resp.Body != nil {
				_ = resp.Body.Close()
			}

			ticker.Stop()
			if pingTicker != nil {
				pingTicker.Stop()
			}

			wg.Wait()
		})
	}
	// Ensure gin.Context is not returned to Gin's pool while any stream goroutine can still use it.
	defer cleanup()

	scanner.Split(bufio.ScanLines)
	copyCodexSSEHeaders(c, resp)
	SetEventStreamHeaders(c)

	ctx = context.WithValue(ctx, "stop_chan", stopChan)

	// Handle ping data sending with improved error handling
	if pingEnabled && pingTicker != nil {
		wg.Add(1)
		gopool.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					logger.LogError(c, common.LogText("ping goroutine panic: %v", r))
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("ping panic: %v", r))
					stop()
				}
				logger.LogDebug(c, "ping goroutine exited")
				wg.Done()
			}()

			// 添加超时保护，防止 goroutine 无限运行
			maxPingDuration := 30 * time.Minute // 最大 ping 持续时间
			pingTimeout := time.NewTimer(maxPingDuration)
			defer pingTimeout.Stop()

			for {
				select {
				case <-pingTicker.C:
					var err error
					func() {
						writeMutex.Lock()
						defer writeMutex.Unlock()
						ExtendWriteDeadline(c)
						err = PingData(c)
					}()
					if err != nil {
						logger.LogError(c, common.LogText("ping data error: %s", err.Error()))
						if disconnectTimeout > 0 {
							disconnect(err)
						} else {
							info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPingFail, err)
						}
						return
					}
					logger.LogDebug(c, "ping data sent")
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				case <-c.Request.Context().Done():
					// 监听客户端断开连接
					return
				case <-clientGone:
					return
				case <-pingTimeout.C:
					logger.LogError(c, common.LogText("ping goroutine max duration reached"))
					return
				}
			}
		})
	}

	dataChan := make(chan string, 10)

	wg.Add(1)
	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LogError(c, common.LogText("data handler goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("handler panic: %v", r))
			}
			stop()
			wg.Done()
		}()
		sr := newStreamResult(info.StreamStatus)
		sr.clientGone = clientGone
		sr.disconnect = disconnect
		for data := range dataChan {
			sr.reset()
			if err := c.Request.Context().Err(); err != nil {
				disconnect(err)
			}
			func() {
				writeMutex.Lock()
				defer writeMutex.Unlock()
				ExtendWriteDeadline(c)
				dataHandler(data, sr)
			}()
			if sr.IsStopped() {
				return
			}
		}
	})

	// Scanner goroutine with improved error handling
	wg.Add(1)
	common.RelayCtxGo(ctx, func() {
		defer func() {
			close(dataChan)
			if r := recover(); r != nil {
				logger.LogError(c, common.LogText("scanner goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("scanner panic: %v", r))
			}
			stop()
			logger.LogDebug(c, "scanner goroutine exited")
			wg.Done()
		}()

		for scanner.Scan() {
			// 检查是否需要停止
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			default:
			}

			ticker.Reset(streamingTimeout)
			data := scanner.Text()
			logger.LogDebug(c, "stream scanner data: %s", data)

			if len(data) < 6 {
				continue
			}
			if data[:5] != "data:" && data[:6] != "[DONE]" {
				continue
			}
			data = data[5:]
			data = strings.TrimSpace(data)
			if data == "" {
				continue
			}
			if !strings.HasPrefix(data, "[DONE]") {
				info.SetFirstResponseTime()
				info.ReceivedResponseCount++

				select {
				case dataChan <- data:
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				}
			} else {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				logger.LogDebug(c, "received [DONE], stopping scanner")
				return
			}
		}

		if err := scanner.Err(); err != nil {
			if err != io.EOF {
				logger.LogError(c, common.LogText("scanner error: %s", err.Error()))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
			}
		}
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	})

	// The drain deadline is independent of upstream activity and cannot be extended by new chunks.
	clientDone := c.Request.Context().Done()
	disconnectSignal := (<-chan struct{})(clientGone)
	var drainDeadline <-chan time.Time
waitStream:
	for {
		select {
		case <-ticker.C:
			info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
			break waitStream
		case <-stopChan:
			break waitStream
		case <-clientDone:
			disconnect(c.Request.Context().Err())
			clientDone = nil
		case <-disconnectSignal:
			if disconnectTimeout == 0 {
				break waitStream
			}
			disconnectSignal = nil
			clientDone = nil
			timer := time.NewTimer(disconnectTimeout)
			defer timer.Stop()
			drainDeadline = timer.C
		case <-drainDeadline:
			info.StreamStatus.RecordError("upstream usage collection timed out after client disconnect")
			break waitStream
		}
	}

	cleanup()
	select {
	case <-clientGone:
		info.StreamStatus.ClientDisconnected = true
	default:
	}
	if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
		logger.LogInfo(c, common.LogText("stream ended: %s", info.StreamStatus.Summary()))
	} else {
		logger.LogError(c, common.LogText("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
	}
}
