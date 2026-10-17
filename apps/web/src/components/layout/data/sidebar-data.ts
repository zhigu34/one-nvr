import {
  LayoutDashboard,
  Video,
  MonitorPlay,
  Film,
  HardDrive,
  Users,
  Settings,
  Activity,
  CalendarClock,
  Bell,
  ScanEye,
} from 'lucide-react'
import type { NavGroup } from '../types'

export const sidebarData: { navGroups: NavGroup[] } = {
  navGroups: [
    {
      title: '监控',
      items: [
        { title: '总览', url: '/', icon: LayoutDashboard, everyRole: true },
        { title: '实时预览', url: '/live', icon: MonitorPlay, everyRole: true },
        { title: '事件', url: '/events', icon: Bell, everyRole: true },
        {
          // One "recordings" entry holding playback and file management, as the
          // product decided; both tabs live at /recordings.
          title: '录像',
          url: '/recordings',
          icon: Film,
          everyRole: true,
        },
      ],
    },
    {
      title: '配置',
      items: [
        { title: '通道管理', url: '/channels', icon: Video, everyRole: true },
        {
          title: '检测配置',
          url: '/detection',
          icon: ScanEye,
        },
        {
          title: '录像计划',
          url: '/recording-plan',
          icon: CalendarClock,
          everyRole: true,
        },
        {
          // One "storage" entry holding pools and archive targets: they answer
          // one question between them ("is this recording safe, and where?"),
          // and splitting them meant switching menus to trace a recording from
          // the disk it landed on to the copy that protects it.
          title: '存储',
          url: '/storage',
          icon: HardDrive,
        },
      ],
    },
    {
      title: '系统',
      items: [
        { title: '用户与权限', url: '/users', icon: Users },
        { title: '系统设置', url: '/settings', icon: Settings },
        { title: '运维与审计', url: '/operations', icon: Activity },
      ],
    },
  ],
}
