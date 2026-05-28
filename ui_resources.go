package main

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

const mcpAppMIMEType = "text/html;profile=mcp-app"

func (s *mcpServer) registerUIResources() {
	s.mcpServer.AddResource(
		mcp.NewResource(
			"ui://call-hierarchy/graph",
			"Call Hierarchy Graph",
			mcp.WithMIMEType(mcpAppMIMEType),
			mcp.WithResourceDescription("Interactive call hierarchy graph for callers and callees results."),
		),
		func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "ui://call-hierarchy/graph",
					MIMEType: mcpAppMIMEType,
					Text:     callHierarchyAppHTML,
				},
			}, nil
		},
	)

	s.mcpServer.AddResource(
		mcp.NewResource(
			"ui://diagnostics/dashboard",
			"Diagnostics Dashboard",
			mcp.WithMIMEType(mcpAppMIMEType),
			mcp.WithResourceDescription("Interactive dashboard for language server diagnostics."),
		),
		func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "ui://diagnostics/dashboard",
					MIMEType: mcpAppMIMEType,
					Text:     diagnosticsDashboardAppHTML,
				},
			}, nil
		},
	)

	coreLogger.Debug("Registered MCP App UI resources: %s, %s",
		"ui://call-hierarchy/graph",
		"ui://diagnostics/dashboard",
	)
}

func mcpAppBootstrapScript(renderFunction string) string {
	return fmt.Sprintf(`<script>
const state = { data: null };
function getStructuredPayload(event) {
  const msg = event && event.data;
  if (!msg || typeof msg !== "object") return null;
  if (msg.structuredContent) return msg.structuredContent;
  if (msg.result && msg.result.structuredContent) return msg.result.structuredContent;
  return null;
}
window.addEventListener("message", (event) => {
  const payload = getStructuredPayload(event);
  if (!payload) return;
  state.data = payload;
  %s(payload);
});
document.addEventListener("DOMContentLoaded", () => {
  const empty = document.querySelector("[data-empty]");
  if (empty) empty.hidden = false;
});
</script>`, renderFunction)
}

var callHierarchyAppHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Call Hierarchy</title>
<style>
:root { color-scheme: light dark; font-family: Inter, ui-sans-serif, system-ui, sans-serif; }
body { margin: 0; background: Canvas; color: CanvasText; }
.shell { display: grid; grid-template-columns: 260px 1fr; min-height: 100vh; }
aside { border-right: 1px solid color-mix(in srgb, CanvasText 14%, transparent); padding: 16px; }
main { padding: 18px; overflow: auto; }
h1 { font-size: 18px; margin: 0 0 12px; }
.stat { display: grid; grid-template-columns: 1fr auto; gap: 8px; font-size: 13px; margin: 8px 0; }
.graph { display: grid; gap: 10px; min-width: 520px; }
.node { display: grid; grid-template-columns: 90px 1fr; gap: 10px; align-items: center; }
.depth { font-size: 12px; color: color-mix(in srgb, CanvasText 62%, transparent); }
.card { border: 1px solid color-mix(in srgb, CanvasText 14%, transparent); border-radius: 8px; padding: 10px 12px; background: color-mix(in srgb, Canvas 92%, CanvasText 8%); }
.name { font-weight: 650; }
.loc { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; color: color-mix(in srgb, CanvasText 68%, transparent); margin-top: 4px; }
[data-empty] { padding: 24px; color: color-mix(in srgb, CanvasText 62%, transparent); }
@media (max-width: 720px) { .shell { grid-template-columns: 1fr; } aside { border-right: 0; border-bottom: 1px solid color-mix(in srgb, CanvasText 14%, transparent); } }
</style>
</head>
<body>
<div class="shell">
  <aside>
    <h1>Call Hierarchy</h1>
    <div class="stat"><span>Direction</span><strong data-direction>-</strong></div>
    <div class="stat"><span>Total</span><strong data-total>0</strong></div>
    <div class="stat"><span>Max depth</span><strong data-depth>0</strong></div>
  </aside>
  <main>
    <div data-empty hidden>No call hierarchy data received yet.</div>
    <div class="graph" data-graph></div>
  </main>
</div>
<script>
function renderCallHierarchy(data) {
  document.querySelector("[data-empty]").hidden = true;
  document.querySelector("[data-direction]").textContent = data.direction || "-";
  document.querySelector("[data-total]").textContent = data.total || 0;
  document.querySelector("[data-depth]").textContent = data.maxDepth || 0;
  const graph = document.querySelector("[data-graph]");
  graph.innerHTML = "";
  const results = Array.isArray(data.results) ? data.results : [];
  for (const item of results) {
    const row = document.createElement("div");
    row.className = "node";
    row.innerHTML = "<div class=\"depth\">Depth " + (item.depth || 0) + "</div>" +
      "<div class=\"card\"><div class=\"name\"></div><div class=\"loc\"></div></div>";
    row.querySelector(".name").textContent = item.name || "(anonymous)";
    row.querySelector(".loc").textContent = (item.filePath || "") + ":L" + (item.line || 0) + ":C" + (item.column || 0);
    graph.appendChild(row);
  }
}
</script>
` + mcpAppBootstrapScript("renderCallHierarchy") + `
</body>
</html>`

var diagnosticsDashboardAppHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Diagnostics Dashboard</title>
<style>
:root { color-scheme: light dark; font-family: Inter, ui-sans-serif, system-ui, sans-serif; }
body { margin: 0; background: Canvas; color: CanvasText; }
.shell { display: grid; grid-template-rows: auto 1fr; min-height: 100vh; }
header { border-bottom: 1px solid color-mix(in srgb, CanvasText 14%, transparent); padding: 16px 18px; }
h1 { font-size: 18px; margin: 0 0 10px; }
.summary { display: flex; gap: 10px; flex-wrap: wrap; }
.pill { border: 1px solid color-mix(in srgb, CanvasText 14%, transparent); border-radius: 999px; padding: 5px 10px; font-size: 13px; }
main { display: grid; grid-template-columns: minmax(280px, 380px) 1fr; min-height: 0; }
.list { border-right: 1px solid color-mix(in srgb, CanvasText 14%, transparent); overflow: auto; }
.diag { padding: 12px 14px; border-bottom: 1px solid color-mix(in srgb, CanvasText 10%, transparent); }
.sev { font-size: 12px; font-weight: 700; }
.ERROR { color: #c51f35; } .WARNING { color: #9a6200; } .INFO { color: #1769aa; } .HINT { color: #55752f; }
.msg { margin-top: 4px; font-size: 14px; }
.loc { margin-top: 4px; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; color: color-mix(in srgb, CanvasText 65%, transparent); }
pre { margin: 0; padding: 16px; overflow: auto; font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 12px; line-height: 1.55; }
[data-empty] { padding: 24px; color: color-mix(in srgb, CanvasText 62%, transparent); }
@media (max-width: 760px) { main { grid-template-columns: 1fr; } .list { border-right: 0; border-bottom: 1px solid color-mix(in srgb, CanvasText 14%, transparent); } }
</style>
</head>
<body>
<div class="shell">
  <header>
    <h1 data-file>Diagnostics</h1>
    <div class="summary">
      <span class="pill">Total <strong data-total>0</strong></span>
      <span class="pill ERROR">Errors <strong data-errors>0</strong></span>
      <span class="pill WARNING">Warnings <strong data-warnings>0</strong></span>
      <span class="pill INFO">Info <strong data-info>0</strong></span>
      <span class="pill HINT">Hints <strong data-hints>0</strong></span>
    </div>
  </header>
  <main>
    <section class="list"><div data-empty hidden>No diagnostics data received yet.</div></section>
    <pre data-source></pre>
  </main>
</div>
<script>
function renderDiagnostics(data) {
  document.querySelector("[data-empty]").hidden = true;
  document.querySelector("[data-file]").textContent = data.filePath || "Diagnostics";
  document.querySelector("[data-total]").textContent = data.total || 0;
  document.querySelector("[data-errors]").textContent = data.errorCount || 0;
  document.querySelector("[data-warnings]").textContent = data.warningCount || 0;
  document.querySelector("[data-info]").textContent = data.infoCount || 0;
  document.querySelector("[data-hints]").textContent = data.hintCount || 0;
  const list = document.querySelector(".list");
  list.querySelectorAll(".diag").forEach((node) => node.remove());
  for (const item of Array.isArray(data.items) ? data.items : []) {
    const row = document.createElement("article");
    row.className = "diag";
    row.innerHTML = "<div class=\"sev\"></div><div class=\"msg\"></div><div class=\"loc\"></div>";
    row.querySelector(".sev").className = "sev " + (item.severity || "");
    row.querySelector(".sev").textContent = item.severity || "UNKNOWN";
    row.querySelector(".msg").textContent = item.message || "";
    row.querySelector(".loc").textContent = "L" + (item.line || 0) + ":C" + (item.column || 0);
    list.appendChild(row);
  }
  const source = Array.isArray(data.contextLines) ? data.contextLines : [];
  document.querySelector("[data-source]").textContent = source.map((line) => String(line.line).padStart(4, " ") + " | " + line.content).join("\n");
}
</script>
` + mcpAppBootstrapScript("renderDiagnostics") + `
</body>
</html>`
