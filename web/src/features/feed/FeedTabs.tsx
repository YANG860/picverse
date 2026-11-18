import type { FeedTab } from '@/features/models'

const TABS: { key: FeedTab; label: string }[] = [
  { key: 'recommend', label: '推荐' },
  { key: 'hot', label: '热门' },
  { key: 'following', label: '关注' },
]

/** 信息流顶部 tab（视觉稿：下划线式切换） */
export default function FeedTabs({ active, onChange }: { active: FeedTab; onChange: (tab: FeedTab) => void }) {
  return (
    <nav className="flex items-center justify-center gap-8 border-b">
      {TABS.map((t) => (
        <button
          key={t.key}
          type="button"
          onClick={() => onChange(t.key)}
          className={
            'relative -mb-px border-b-2 px-1 py-3 text-sm transition-colors ' +
            (active === t.key ? 'border-primary font-medium text-foreground' : 'border-transparent text-muted-foreground hover:text-foreground')
          }
        >
          {t.label}
        </button>
      ))}
    </nav>
  )
}
