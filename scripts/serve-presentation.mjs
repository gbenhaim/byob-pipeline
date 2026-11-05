// Serve only the presentation assets; never expose the repository or kubeconfig.
import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";

const root = new URL("../docs/presentation/", import.meta.url);
const assets = new Map([
  ["/", ["index.html", "text/html; charset=utf-8"]],
  ["/index.html", ["index.html", "text/html; charset=utf-8"]],
  ["/styles.css", ["styles.css", "text/css; charset=utf-8"]],
  ["/app.js", ["app.js", "text/javascript; charset=utf-8"]],
  [
    "/presenter-script.md",
    ["presenter-script.md", "text/plain; charset=utf-8"],
  ],
  ["/README.md", ["README.md", "text/plain; charset=utf-8"]],
]);
const port = Number(process.env.PORT || 4173);
if (!Number.isInteger(port) || port < 1 || port > 65535) {
  throw new Error("PORT must be between 1 and 65535");
}

const server = createServer(async (request, response) => {
  const path = new URL(request.url, "http://localhost").pathname;
  const asset = assets.get(path);
  if (!asset || !["GET", "HEAD"].includes(request.method)) {
    response.writeHead(404).end("Not found");
    return;
  }
  try {
    const body = await readFile(new URL(asset[0], root));
    response.writeHead(200, {
      "Content-Type": asset[1],
      "Cache-Control": "no-store",
      "X-Content-Type-Options": "nosniff",
    });
    response.end(request.method === "HEAD" ? undefined : body);
  } catch {
    response.writeHead(500).end("Unable to read presentation asset");
  }
});
server.on("error", (error) => {
  console.error(
    error.code === "EADDRINUSE"
      ? `Port ${port} is busy; use PORT=4174 make presentation`
      : error.message,
  );
  process.exitCode = 1;
});
server.listen(port, "127.0.0.1", () => {
  console.log(`Presentation: http://127.0.0.1:${port}`);
  console.log(`Offline file: ${fileURLToPath(new URL("index.html", root))}`);
  console.log("Arrow keys: slides · N: notes · F: fullscreen · Ctrl+C: stop");
});
