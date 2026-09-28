/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { Expand, ExternalLink, Minimize2 } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'

const TOPUP_SHOP_URL = 'https://wzyp.cn/shop/ZMTJNACJ'

export function TopupCenter() {
  const { t } = useTranslation()
  const [isFullscreen, setIsFullscreen] = useState(false)

  useEffect(() => {
    const syncFullscreen = () => {
      setIsFullscreen(document.fullscreenElement != null)
    }
    document.addEventListener('fullscreenchange', syncFullscreen)
    return () => {
      document.removeEventListener('fullscreenchange', syncFullscreen)
    }
  }, [])

  const toggleFullscreen = () => {
    if (document.fullscreenElement) {
      void document.exitFullscreen()
      return
    }
    void document.documentElement.requestFullscreen()
  }

  return (
    <div className='flex h-full min-h-0 flex-col'>
      <div className='flex shrink-0 flex-wrap items-start justify-between gap-3 border-b px-4 py-3'>
        <div className='min-w-0 space-y-1'>
          <h2 className='text-sm font-semibold'>
            {t('Alipay / WeChat Pay')}
          </h2>
          <p className='text-muted-foreground text-xs leading-5'>
            {t(
              'The top-up center is ready. You can pay with Alipay or WeChat Pay.'
            )}
          </p>
          <p className='text-xs leading-5 text-orange-600 dark:text-orange-400'>
            {t(
              'Blank page or failed to load? Open it in a new window. If it still fails, switch networks and try again.'
            )}
          </p>
        </div>
        <div className='flex shrink-0 items-center gap-2'>
          <Button variant='outline' size='sm' onClick={toggleFullscreen}>
            {isFullscreen ? <Minimize2 /> : <Expand />}
            {isFullscreen ? t('Exit fullscreen') : t('View fullscreen')}
          </Button>
          <Button
            variant='outline'
            size='sm'
            nativeButton={false}
            render={
              <a
                href={TOPUP_SHOP_URL}
                target='_blank'
                rel='noopener noreferrer'
              />
            }
          >
            <ExternalLink />
            {t('Open in new window')}
          </Button>
        </div>
      </div>
      <iframe
        src={TOPUP_SHOP_URL}
        title={t('Top-up Center')}
        className='min-h-0 w-full flex-1 border-0 bg-background'
      />
    </div>
  )
}
