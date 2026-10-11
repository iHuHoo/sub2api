export function resolveCompletedSetupRedirectPath(isAuthenticated: boolean, isAdmin: boolean, isSupplier = false): string {
  if (!isAuthenticated) {
    return '/login'
  }

  if (isSupplier) return '/supplier/accounts'
  return isAdmin ? '/admin/dashboard' : '/dashboard'
}
