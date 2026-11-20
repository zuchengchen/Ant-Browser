import { Navigate } from 'react-router-dom'

// Preserve bookmarks to the former tag management page.
export function TagManagementPage() {
  return <Navigate to="/browser/list?groups=open" replace />
}
