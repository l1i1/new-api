import { Megaphone } from 'lucide-react'

import { RichContent } from '@/components/rich-content'
import { Card, CardContent } from '@/components/ui/card'
import { IconBadge } from '@/components/ui/icon-badge'

interface PartnerNoticeCardProps {
  notice: string
}

/**
 * Renders the partner announcement block for white-labeled users.
 * Content is partner-authored Markdown/HTML; language blocks resolve
 * client-side through RichContent. Renders nothing when empty.
 */
export function PartnerNoticeCard(props: PartnerNoticeCardProps) {
  const content = props.notice.trim()
  if (!content) return null
  return (
    <Card data-card-hover='false' className='bg-muted/20 py-0'>
      <CardContent className='p-0'>
        <div className='flex min-w-0 items-start gap-2.5 p-3 sm:p-4'>
          <IconBadge tone='chart-3'>
            <Megaphone />
          </IconBadge>
          <div className='min-w-0 flex-1 text-sm'>
            <RichContent content={content} mode='markdown' breaks />
          </div>
        </div>
      </CardContent>
    </Card>
  )
}
