const assert = require('node:assert/strict')
const test = require('node:test')

const {
  analyzeVersions,
  classifyRiskFiles,
  reconcileTrackingIssue,
} = require('./upstream-release-monitor')

test('selects the newest stable upstream version and counts every missed version', () => {
  const result = analyzeVersions({
    baselineTag: 'v0.1.183',
    tags: ['v0.1.185', 'v0.1.184', 'v0.1.184-rc.1', 'v0.1.183-aox.0.0.6'],
    releases: ['v0.1.184'],
  })

  assert.deepEqual(result, {
    latestTag: 'v0.1.185',
    latestIsRelease: false,
    versionsBehind: 2,
    missedTags: ['v0.1.184', 'v0.1.185'],
  })
})

test('classifies payment and billing changes as high risk', () => {
  const result = classifyRiskFiles([
    'README.md',
    'backend/internal/service/gateway_usage_billing.go',
    'backend/internal/service/payment_order.go',
    'backend/ent/schema/user.go',
    'frontend/src/components/layout/AppHeader.vue',
    'frontend/src/views/user/KeysView.vue',
  ])

  assert.deepEqual(result, [
    'backend/internal/service/gateway_usage_billing.go',
    'backend/internal/service/payment_order.go',
    'backend/ent/schema/user.go',
    'frontend/src/components/layout/AppHeader.vue',
    'frontend/src/views/user/KeysView.vue',
  ])
})

test('creates one rolling issue, leaves unchanged state quiet, and closes it when caught up', async () => {
  const calls = []
  const github = fakeGithub(calls)
  const context = { repo: { owner: 'iHuHoo', repo: 'sub2api' } }
  const state = {
    marker: '<!-- upstream-sync:base=v0.1.183;latest=v0.1.185 -->',
    body: 'tracking body',
    caughtUpBody: 'caught up body',
  }

  await reconcileTrackingIssue({ github, context, state, behind: true })
  assert.deepEqual(calls, [
    ['create', {
      owner: 'iHuHoo',
      repo: 'sub2api',
      title: '[Upstream Sync] Tracking',
      body: `tracking body\n\n${state.marker}`,
    }],
  ])

  calls.length = 0
  github.setIssue({
    number: 12,
    state: 'open',
    title: '[Upstream Sync] Tracking',
    body: `tracking body\n\n${state.marker}`,
  })
  await reconcileTrackingIssue({ github, context, state, behind: true })
  assert.deepEqual(calls, [])

  await reconcileTrackingIssue({ github, context, state, behind: false })
  assert.deepEqual(calls, [
    ['comment', { owner: 'iHuHoo', repo: 'sub2api', issue_number: 12, body: 'caught up body' }],
    ['update', { owner: 'iHuHoo', repo: 'sub2api', issue_number: 12, state: 'closed' }],
  ])
})

function fakeGithub(calls) {
  let issue = null
  return {
    setIssue(value) {
      issue = value
    },
    paginate: async () => (issue ? [issue] : []),
    rest: {
      issues: {
        listForRepo: Symbol('listForRepo'),
        create: async (args) => calls.push(['create', args]),
        update: async (args) => calls.push(['update', args]),
        createComment: async (args) => calls.push(['comment', args]),
      },
    },
  }
}
