const MAX_BODY = 32 * 1024;
const REPO = "GodYtteria/gobridgesync-desktop-staging";
const SECRET = /(ya29\.[\w-]+|1\/\/[\w-]{20,}|AIza[\w-]{30,}|ghp_[\w]{20,}|github_pat_[\w]{20,}|-----BEGIN [A-Z ]*PRIVATE KEY-----|(?:password|secret|token)\s*[=:]\s*\S+)/gi;

function clean(value, limit) {
  return String(value ?? "").replace(SECRET, "[redacted]").slice(0, limit);
}

async function sha(text) {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(text));
  return [...new Uint8Array(digest)].slice(0, 6).map((b) => b.toString(16).padStart(2, "0")).join("");
}

async function github(env, path, init = {}) {
  return fetch(`https://api.github.com/repos/${REPO}${path}`, {
    ...init,
    headers: {
      authorization: `Bearer ${env.GITHUB_TOKEN}`,
      accept: "application/vnd.github+json",
      "user-agent": "gobridgesync-reports",
      "content-type": "application/json",
    },
  });
}

export default {
  async fetch(request, env) {
    if (request.method !== "POST" || new URL(request.url).pathname !== "/report") {
      return new Response("not found", { status: 404 });
    }
    const raw = await request.text();
    if (raw.length > MAX_BODY) {
      return new Response("too large", { status: 413 });
    }
    let report;
    try {
      report = JSON.parse(raw);
    } catch {
      return new Response("bad json", { status: 400 });
    }
    const app = clean(report.app, 60);
    const version = clean(report.version, 40);
    const os = clean(report.os, 40);
    const error = clean(report.error, 500);
    const log = clean(report.log, 20000);
    if (!error) {
      return new Response("missing error", { status: 400 });
    }
    const key = await sha(`${app}|${error.replace(/\d+/g, "#")}`);
    const tag = `report:${key}`;
    const search = await github(env, `/issues?state=open&labels=problem-report&per_page=100`);
    const open = search.ok ? await search.json() : [];
    const existing = open.find((i) => i.title.includes(tag));
    const body = `**App:** ${app} ${version}\n**OS:** ${os}\n\n**Problem:**\n\`\`\`\n${error}\n\`\`\`\n\n**Recent log:**\n\`\`\`\n${log}\n\`\`\``;
    const res = existing
      ? await github(env, `/issues/${existing.number}/comments`, { method: "POST", body: JSON.stringify({ body }) })
      : await github(env, "/issues", {
          method: "POST",
          body: JSON.stringify({ title: `${error.slice(0, 80)} [${tag}]`, body, labels: ["problem-report"] }),
        });
    return new Response(res.ok ? "ok" : "upstream error", { status: res.ok ? 202 : 502 });
  },
};
