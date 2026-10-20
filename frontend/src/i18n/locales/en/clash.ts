export default {
 title: 'Clash subscription', supplierScope: 'Exports only active proxies assigned by an administrator, excluding your own proxies.', adminScope: 'Exports only the administrator pool without a supplier owner or assignment.',
 warning: 'The link and profile contain proxy credentials. Do not share them. Revoking a link cannot recall downloaded credentials; immediate blocking requires rotating upstream credentials.',
 count: '{count} exportable nodes; {skipped} invalid nodes skipped.', active: 'Active subscription', inactive: 'Revoked or expired subscription', absent: 'No subscription yet', expires: 'Expires',
 zero: 'No exportable nodes. Create, reset and copy are unavailable. Clients may retain an old profile.', duration: 'Validity', days: '{count} days',
 once: 'This link is shown only in this dialog. It cannot be recovered after closing.', noRecover: 'The previous link cannot be viewed. Reset explicitly if needed, then import the new link in your client.',
 import: 'Clash Verge → Profiles → New remote profile → paste link → update and select. A 60-minute update interval is suggested; timely refresh or removal of old nodes is not guaranteed. This profile routes traffic through the selected proxy.',
 confirm: 'Proceed? The previous link will stop working, but downloaded profiles and proxy credentials cannot be recalled.', create: 'Create link', reset: 'Reset link', revoke: 'Revoke link', copy: 'Copy link', close: 'Close', proceed: 'Confirm',
 copied: 'Copied', unavailable: 'Subscriptions are disabled or temporarily unavailable.', conflict: 'Subscription state changed. Review the latest state and retry.', retry: 'The operation was not confirmed. State has been refreshed; check it before retrying. Links are never reset automatically.'
}
