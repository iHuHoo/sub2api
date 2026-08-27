const fs = require('node:fs')
const path = require('node:path')

const ISSUE_TITLE = '[Upstream Sync] Tracking'
const UPSTREAM_OWNER = 'Wei-Shaw'
const UPSTREAM_REPO = 'sub2api'
const STABLE_VERSION = /^v(\d+)\.(\d+)\.(\d+)$/
const RISK_PATH = /(auth|security|billing|payment|balance|quota|subscription|affiliate|refund|redeem|usage|api.?key|keys?view|pricing|cost|rate.?multiplier|ent\/schema\/(user|group)\.go|appheader)/i

function parseVersion(tag) {
  const match = STABLE_VERSION.exec(tag)
  return match ? match.slice(1).map(Number) : null
}

function compareVersions(left, right) {
  const a = parseVersion(left)
  const b = parseVersion(right)
  if (!a || !b) throw new Error(`Invalid stable version: ${!a ? left : right}`)
  for (let i = 0; i < a.length; i += 1) {
    if (a[i] !== b[i]) return a[i] - b[i]
  }
  return 0
}

function analyzeVersions({ baselineTag, tags, releases }) {
  if (!parseVersion(baselineTag)) throw new Error(`Invalid baseline tag: ${baselineTag}`)

  const stableTags = [...new Set([...tags, ...releases].filter(parseVersion))]
    .sort(compareVersions)
  if (stableTags.length === 0) throw new Error('Upstream has no stable vX.Y.Z tags')

  const latestTag = stableTags.at(-1)
  const missedTags = stableTags.filter((tag) => compareVersions(tag, baselineTag) > 0)
  return {
    latestTag,
    latestIsRelease: releases.includes(latestTag),
    versionsBehind: missedTags.length,
    missedTags,
  }
}

function classifyRiskFiles(files) {
  return files.filter((file) => RISK_PATH.test(file))
}

async function reconcileTrackingIssue({ github, context, state, behind }) {
  const repo = context.repo
  const issues = await github.paginate(github.rest.issues.listForRepo, {
    ...repo,
    state: 'all',
    per_page: 100,
  })
  const issue = issues.find((item) => !item.pull_request && item.title === ISSUE_TITLE)

  if (!behind) {
    if (issue?.state === 'open') {
      await github.rest.issues.createComment({ ...repo, issue_number: issue.number, body: state.caughtUpBody })
      await github.rest.issues.update({ ...repo, issue_number: issue.number, state: 'closed' })
    }
    return
  }

  const body = `${state.body}\n\n${state.marker}`
  if (!issue) {
    await github.rest.issues.create({ ...repo, title: ISSUE_TITLE, body })
  } else if (issue.state !== 'open' || !issue.body?.includes(state.marker)) {
    await github.rest.issues.update({ ...repo, issue_number: issue.number, state: 'open', body })
  }
}

function buildTrackingState({ baseline, analysis, comparison }) {
  const files = comparison.files?.map((file) => file.filename) ?? []
  const riskFiles = classifyRiskFiles(files)
  const level = analysis.versionsBehind >= 3 ? '阻断新功能发布' : analysis.versionsBehind === 2 ? '高优先级' : '普通提醒'
  const compareURL = `https://github.com/${UPSTREAM_OWNER}/${UPSTREAM_REPO}/compare/${baseline.sha}...${analysis.latestTag}`
  const riskSection = riskFiles.length > 0
    ? riskFiles.slice(0, 50).map((file) => `- \`${file}\``).join('\n')
    : '未发现预设的支付、计费、余额、配额、认证或安全高风险文件。'

  return {
    marker: `<!-- upstream-sync:base=${baseline.tag};latest=${analysis.latestTag} -->`,
    body: [
      '上游发布了新的稳定版本，forx 代码基线需要同步。此 issue 只跟踪代码同步，不代表需要发布或部署。',
      '',
      `- 当前基线：\`${baseline.tag}\`（\`${baseline.sha}\`）`,
      `- 上游最新：\`${analysis.latestTag}\`（${analysis.latestIsRelease ? 'release' : 'tag'}）`,
      `- 落后版本：${analysis.versionsBehind}（${analysis.missedTags.map((tag) => `\`${tag}\``).join('、')}）`,
      `- 落后提交：${comparison.ahead_by ?? comparison.total_commits ?? '未知'}`,
      `- 告警等级：**${level}**`,
      `- [查看上游差异](${compareURL})`,
      '',
      '### 高风险文件',
      '',
      riskSection,
      '',
      '### 处理原则',
      '',
      '- 尽快开同步 PR，CI 通过并人工复核高风险文件后合并。',
      '- 默认不打 aox tag、不发布、不部署。',
      '- 同步完成时更新 `.github/upstream-baseline.json`；监控会自动关闭本 issue。',
    ].join('\n'),
    caughtUpBody: `forx 上游基线已追平至 \`${analysis.latestTag}\`，自动关闭跟踪 issue。`,
  }
}

async function run({ github, context, core, workspace = process.cwd() }) {
  const baselinePath = path.join(workspace, '.github', 'upstream-baseline.json')
  const baseline = JSON.parse(fs.readFileSync(baselinePath, 'utf8'))

  const [tagRows, releaseRows] = await Promise.all([
    github.paginate(github.rest.repos.listTags, { owner: UPSTREAM_OWNER, repo: UPSTREAM_REPO, per_page: 100 }),
    github.paginate(github.rest.repos.listReleases, { owner: UPSTREAM_OWNER, repo: UPSTREAM_REPO, per_page: 100 }),
  ])
  const analysis = analyzeVersions({
    baselineTag: baseline.tag,
    tags: tagRows.map((row) => row.name),
    releases: releaseRows.filter((row) => !row.draft && !row.prerelease).map((row) => row.tag_name),
  })

  if (analysis.versionsBehind === 0) {
    await reconcileTrackingIssue({
      github,
      context,
      behind: false,
      state: { caughtUpBody: `forx 上游基线已追平至 \`${baseline.tag}\`，自动关闭跟踪 issue。` },
    })
    core.info(`Up to date with ${baseline.tag}`)
    return
  }

  const { data: comparison } = await github.rest.repos.compareCommitsWithBasehead({
    owner: UPSTREAM_OWNER,
    repo: UPSTREAM_REPO,
    basehead: `${baseline.sha}...${analysis.latestTag}`,
    per_page: 100,
  })
  const state = buildTrackingState({ baseline, analysis, comparison })
  await reconcileTrackingIssue({ github, context, state, behind: true })
  core.warning(`Upstream ${analysis.latestTag} is ${analysis.versionsBehind} version(s) ahead`)
}

module.exports = {
  analyzeVersions,
  buildTrackingState,
  classifyRiskFiles,
  reconcileTrackingIssue,
  run,
}
