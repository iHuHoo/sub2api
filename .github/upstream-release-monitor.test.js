const assert = require('node:assert/strict')
const test = require('node:test')

const {
  analyzeVersions,
  classifyRiskFiles,
  reconcileTrackingIssues,
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

test('creates a separate issue for every missed version', async () => {
  const calls = []
  const github = fakeGithub(calls, [])
  const context = { repo: { owner: 'iHuHoo', repo: 'sub2api' } }
  const states = [trackingState('v0.2.4'), trackingState('v0.2.5')]

  await reconcileTrackingIssues({ github, context, baselineTag: 'v0.2.3', states })
  assert.deepEqual(calls, [
    ['create', {
      owner: 'iHuHoo',
      repo: 'sub2api',
      title: '[Upstream Sync] v0.2.4',
      body: 'tracking v0.2.4\n\n<!-- upstream-sync:version=v0.2.4 -->',
    }],
    ['create', {
      owner: 'iHuHoo',
      repo: 'sub2api',
      title: '[Upstream Sync] v0.2.5',
      body: 'tracking v0.2.5\n\n<!-- upstream-sync:version=v0.2.5 -->',
    }],
  ])
})

test('does not reopen a closed version issue', async () => {
  const calls = []
  const github = fakeGithub(calls, [{
    number: 24,
    state: 'closed',
    title: 'manually renamed v0.2.4 sync',
    body: 'tracking v0.2.4\n\n<!-- upstream-sync:version=v0.2.4 -->',
  }])

  await reconcileTrackingIssues({
    github,
    context: { repo: { owner: 'iHuHoo', repo: 'sub2api' } },
    baselineTag: 'v0.2.3',
    states: [trackingState('v0.2.4')],
  })
  assert.deepEqual(calls, [])
})

test('closes only version issues covered by the baseline', async () => {
  const calls = []
  const github = fakeGithub(calls, [
    { number: 24, state: 'open', title: '[Upstream Sync] v0.2.4', body: '' },
    { number: 25, state: 'open', title: '[Upstream Sync] v0.2.5', body: '' },
  ])

  await reconcileTrackingIssues({
    github,
    context: { repo: { owner: 'iHuHoo', repo: 'sub2api' } },
    baselineTag: 'v0.2.4',
    states: [],
  })
  assert.deepEqual(calls, [
    ['comment', {
      owner: 'iHuHoo',
      repo: 'sub2api',
      issue_number: 24,
      body: 'forx 上游基线已追平至 `v0.2.4`，自动关闭此版本的跟踪 issue。',
    }],
    ['update', { owner: 'iHuHoo', repo: 'sub2api', issue_number: 24, state: 'closed' }],
  ])
})

function trackingState(version) {
  return {
    version,
    title: `[Upstream Sync] ${version}`,
    marker: `<!-- upstream-sync:version=${version} -->`,
    body: `tracking ${version}`,
  }
}

function fakeGithub(calls, issues) {
  return {
    paginate: async () => issues,
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
