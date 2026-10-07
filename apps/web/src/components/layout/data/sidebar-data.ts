import {
  LayoutDashboard,
  Video,
  MonitorPlay,
  HardDrive,
  Users,
  Settings,
  Activity,
} from 'lucide-react'
import type { NavGroup } from '../types'

export const sidebarData: { navGroups: NavGroup[] } = {
  navGroups: [
    {
      title: 'one-nvr',
      items: [
        { title: '总览', url: '/', icon: LayoutDashboard },
        { title: '实时预览', url: '/live', icon: MonitorPlay },
        { title: '通道管理', url: '/channels', icon: Video },
        { title: '存储池', url: '/storage-pools', icon: HardDrive },
        { title: '用户与权限', url: '/users', icon: Users },
        { title: '系统设置', url: '/settings', icon: Settings },
        { title: '运维与审计', url: '/operations', icon: Activity },
      ],
    },
  ],
}
