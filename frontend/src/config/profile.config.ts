import { projectConfig } from './projectBase.config'
import { PROJECT_GITHUB_URL } from './links'

export type ProfileIconKey = 'globe' | 'github' | 'external-link'

export interface ProjectProfileActionConfig {
  label: string
  href: string
  icon: ProfileIconKey
}

export interface ProjectProfileConfig {
  name: string
  introBadge: string
  introText: string
  techStack: string[]
  description: string
  actions: ProjectProfileActionConfig[]
}

export interface ProfilePageLocalConfig {
  project: ProjectProfileConfig
}

export const profilePageConfig: ProfilePageLocalConfig = {
  project: {
    name: projectConfig.name,
    introBadge: projectConfig.name,
    introText: '是一个面向多账号隔离、代理绑定和本地环境管理的桌面浏览器工具。',
    techStack: ['Wails', 'React', 'TypeScript'],
    description: '项目当前聚焦浏览器实例隔离、代理池配置、浏览器内核管理、标签检索和快捷启动等核心能力，适合跨境电商、社媒运营、本地测试以及需要统一管理浏览器环境的团队场景。',
    actions: [
      {
        label: '查看源码',
        href: PROJECT_GITHUB_URL,
        icon: 'github',
      },
      {
        label: '下载发布版',
        href: `${PROJECT_GITHUB_URL}/releases`,
        icon: 'globe',
      },
    ],
  },
}

export default profilePageConfig
