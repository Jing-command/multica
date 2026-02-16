import { useState } from 'react'
import type { QueuedLocalMessage } from '../hooks/use-local-chat'

interface QueuedMessageBarProps {
  messages: QueuedLocalMessage[]
  isRunning: boolean
  onRemove: (id: string) => void
  onClear: () => void
}

export function QueuedMessageBar({ messages, isRunning, onRemove, onClear }: QueuedMessageBarProps) {
  const [expanded, setExpanded] = useState(false)

  const statusText = isRunning
    ? 'Agent is running. Queued messages will send automatically.'
    : 'Queued messages are being sent.'

  if (messages.length === 0) return null
  const firstMessage = messages[0]
  const firstMessagePreview =
    firstMessage.text.length <= 120
      ? firstMessage.text
      : `${firstMessage.text.slice(0, 120)}...`

  return (
    <div className="container px-4 pb-2">
      <div className="rounded-lg border bg-muted/40">
        <div className="flex items-center justify-between gap-2 px-3 pt-2 pb-1">
          <div className="text-xs font-medium text-foreground/80">
            {messages.length} queued message{messages.length > 1 ? 's' : ''}
          </div>
          <div className="flex items-center gap-2">
            {messages.length > 1 && (
              <button
                onClick={() => setExpanded((prev) => !prev)}
                className="text-xs text-muted-foreground hover:text-foreground transition-colors"
              >
                {expanded ? 'Collapse' : 'Expand'}
              </button>
            )}
            <button
              onClick={onClear}
              className="text-xs text-muted-foreground hover:text-foreground transition-colors"
            >
              Clear
            </button>
          </div>
        </div>
        <div className="px-3 pb-2 text-xs text-muted-foreground">{statusText}</div>
        {expanded ? (
          <div className="px-2 pb-2">
            <div className="max-h-40 space-y-1 overflow-y-auto pr-1">
              {messages.map((item) => (
                <div key={item.id} className="flex items-start justify-between gap-2 rounded-md bg-background/70 px-2 py-1.5">
                  <div className="text-xs text-foreground/85 break-all">{item.text}</div>
                  <button
                    onClick={() => onRemove(item.id)}
                    className="shrink-0 text-xs text-muted-foreground hover:text-foreground transition-colors"
                  >
                    Remove
                  </button>
                </div>
              ))}
            </div>
          </div>
        ) : (
          <div className="px-2 pb-2 space-y-1">
            <div className="flex items-start justify-between gap-2 rounded-md bg-background/70 px-2 py-1.5">
              <div className="text-xs text-foreground/85 break-all">{firstMessagePreview}</div>
              <button
                onClick={() => onRemove(firstMessage.id)}
                className="shrink-0 text-xs text-muted-foreground hover:text-foreground transition-colors"
              >
                Remove
              </button>
            </div>
            {messages.length > 1 && (
              <div className="px-1 text-xs text-muted-foreground">
                +{messages.length - 1} more
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
