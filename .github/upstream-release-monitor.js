const fs = require('node:fs')
const path = require('node:path')

const ISSUE_TITLE_PREFIX = '[Upstream Sync] '
const ISSUE_TITLE_PATTERN = /^\[Upstream Sync\] (v\d+\.\d+\.\d+)$/
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

async function reconcileTrackingIssues({ github, context, baselineTag, states }) {
  const repo = context.repo
  const issues = await github.paginate(github.rest.issues.listForRepo, {
    ...repo,
    state: 'all',
    per_page: 100,
  })

  for (const state of states) {
    const body = `${state.body}\n\n${state.marker}`
    const issue = issues.find((item) => !item.pull_request && (
      item.title === state.title || item.body?.includes(state.marker)
    ))
    if (!issue) {
      await github.rest.issues.create({ ...repo, title: state.title, body })
    } else if (issue.state === 'open' && issue.body !== body) {
      await github.rest.issues.update({ ...repo, issue_number: issue.number, body })
    }
  }

  for (const issue of issues) {
    const version = !issue.pull_request && issue.state === 'open'
      ? ISSUE_TITLE_PATTERN.exec(issue.title)?.[1]
      : null
    if (version && compareVersions(version, baselineTag) <= 0) {
      const body = `forx 上游基线已追平至 \`${baselineTag}\`，自动关闭此版本的跟踪 issue。`
      await github.rest.issues.createComment({ ...repo, issue_number: issue.number, body })
      await github.rest.issues.update({ ...repo, issue_number: issue.number, state: 'closed' })
    }
  }
}

function buildTrackingState({ baseline, targetTag, targetIsRelease, versionsBehind, comparison }) {
  const files = comparison.files?.map((file) => file.filename) ?? []
  const riskFiles = classifyRiskFiles(files)
  const level = versionsBehind >= 3 ? '阻断新功能发布' : versionsBehind === 2 ? '高优先级' : '普通提醒'
  const compareURL = `https://github.com/${UPSTREAM_OWNER}/${UPSTREAM_REPO}/compare/${baseline.sha}...${targetTag}`
  const riskSection = riskFiles.length > 0
    ? riskFiles.slice(0, 50).map((file) => `- \`${file}\``).join('\n')
    : '未发现预设的支付、计费、余额、配额、认证或安全高风险文件。'

  return {
    version: targetTag,
    title: `${ISSUE_TITLE_PREFIX}${targetTag}`,
    marker: `<!-- upstream-sync:version=${targetTag} -->`,
    body: [
      '上游发布了新的稳定版本，forx 代码基线需要同步。此 issue 只跟踪代码同步，不代表需要发布或部署。',
      '',
      `- 当前基线：\`${baseline.tag}\`（\`${baseline.sha}\`）`,
      `- 跟踪版本：\`${targetTag}\`（${targetIsRelease ? 'release' : 'tag'}）`,
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
  }
}

async function run({ github, context, core, workspace = process.cwd() }) {
  const baselinePath = path.join(workspace, '.github', 'upstream-baseline.json')
  const baseline = JSON.parse(fs.readFileSync(baselinePath, 'utf8'))

  const [tagRows, releaseRows] = await Promise.all([
    github.paginate(github.rest.repos.listTags, { owner: UPSTREAM_OWNER, repo: UPSTREAM_REPO, per_page: 100 }),
    github.paginate(github.rest.repos.listReleases, { owner: UPSTREAM_OWNER, repo: UPSTREAM_REPO, per_page: 100 }),
  ])
  const releases = releaseRows.filter((row) => !row.draft && !row.prerelease).map((row) => row.tag_name)
  const analysis = analyzeVersions({
    baselineTag: baseline.tag,
    tags: tagRows.map((row) => row.name),
    releases,
  })

  const states = await Promise.all(analysis.missedTags.map(async (targetTag) => {
    const { data: comparison } = await github.rest.repos.compareCommitsWithBasehead({
      owner: UPSTREAM_OWNER,
      repo: UPSTREAM_REPO,
      basehead: `${baseline.sha}...${targetTag}`,
      per_page: 100,
    })
    return buildTrackingState({
      baseline,
      targetTag,
      targetIsRelease: releases.includes(targetTag),
      versionsBehind: analysis.versionsBehind,
      comparison,
    })
  }))

  await reconcileTrackingIssues({ github, context, baselineTag: baseline.tag, states })

  if (analysis.versionsBehind === 0) {
    core.info(`Up to date with ${baseline.tag}`)
    return
  }

  core.warning(`Upstream ${analysis.latestTag} is ${analysis.versionsBehind} version(s) ahead`)
}

module.exports = {
  analyzeVersions,
  buildTrackingState,
  classifyRiskFiles,
  reconcileTrackingIssues,
  run,
}
