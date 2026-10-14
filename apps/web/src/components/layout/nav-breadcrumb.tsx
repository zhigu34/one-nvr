import { Link, useLocation } from '@tanstack/react-router'
import { ChevronRight } from 'lucide-react'
import { sidebarData } from './data/sidebar-data'

/**
 * The one breadcrumb line in the topbar: `分组 / 页面`, derived from the same
 * sidebar data that draws the navigation, so the two can never disagree. The
 * page segment links back to itself only when the location carries search
 * state worth keeping — plain text otherwise.
 */
export function NavBreadcrumb() {
  const href = useLocation({ select: (location) => location.href })
  const path = href.split('?')[0]
  for (const group of sidebarData.navGroups) {
    for (const item of group.items) {
      if (item.items) {
        const sub = item.items.find((child) => child.url === path)
        if (sub)
          return (
            <nav aria-label='页面位置' className='flex items-center gap-1.5'>
              <span className='text-sm text-muted-foreground'>
                {item.title}
              </span>
              <ChevronRight aria-hidden className='size-3.5 text-muted-foreground' />
              <Link
                to={sub.url}
                className='text-sm font-medium hover:text-foreground'
              >
                {sub.title}
              </Link>
            </nav>
          )
        continue
      }
      if (item.url === path)
        return (
          <nav aria-label='页面位置' className='flex items-center gap-1.5'>
            <span className='text-sm text-muted-foreground'>{group.title}</span>
            <ChevronRight aria-hidden className='size-3.5 text-muted-foreground' />
            <span className='text-sm font-medium'>{item.title}</span>
          </nav>
        )
    }
  }
  // Setup and other unlisted routes keep a quiet fallback.
  return (
    <nav aria-label='页面位置' className='text-sm text-muted-foreground'>
      one-nvr
    </nav>
  )
}
