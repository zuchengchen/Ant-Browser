import type { BrowserGroup, BrowserGroupInput, BrowserGroupWithCount } from '../types'
import { getBindings } from './runtime'
import { waitForAppReady } from '../../../utils/appReady'

async function groupBindings() {
  await waitForAppReady()
  const bindings: any = await getBindings()
  if (!bindings) throw new Error('实例分组服务不可用，请打开桌面应用后重试')
  return bindings
}
export async function fetchGroups(): Promise<BrowserGroupWithCount[]> {
  const bindings = await groupBindings()
  if (!bindings.BrowserInstanceGroupList) throw new Error('当前版本缺少实例分组接口，请重新打开更新后的应用')
  const groups = await bindings.BrowserInstanceGroupList()
  if (!Array.isArray(groups)) throw new Error('实例分组返回格式无效')
  return groups
}
export async function createGroup(input: BrowserGroupInput): Promise<BrowserGroup> {
  const bindings = await groupBindings()
  if (!bindings.CreateGroup) throw new Error('新建分组接口不可用')
  const group = await bindings.CreateGroup(input)
  if (!group?.groupId) throw new Error('新建分组未返回结果')
  return group
}
export async function updateGroup(groupId: string, input: BrowserGroupInput): Promise<BrowserGroup> {
  const bindings = await groupBindings()
  if (!bindings.UpdateGroup) throw new Error('更新分组接口不可用')
  const group = await bindings.UpdateGroup(groupId, input)
  if (!group?.groupId) throw new Error('更新分组未返回结果')
  return group
}
export async function deleteGroup(groupId: string): Promise<boolean> {
  const bindings = await groupBindings()
  if (!bindings.DeleteGroup) throw new Error('删除分组接口不可用')
  await bindings.DeleteGroup(groupId)
  return true
}
export async function changeProfileGroups(profileIds: string[], groupIds: string[], mode: 'add' | 'remove' | 'set'): Promise<void> {
  const bindings = await groupBindings()
  if (!bindings.BrowserProfileChangeGroups) throw new Error('实例分组接口不可用')
  await bindings.BrowserProfileChangeGroups(profileIds, groupIds, mode)
}
export async function moveInstancesToGroup(profileIds: string[], groupId: string): Promise<boolean> {
  await changeProfileGroups(profileIds, [groupId], 'add')
  return true
}
