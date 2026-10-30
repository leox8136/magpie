// Local page requests and file links stay under the web listener's mount.
// Absolute external URLs, protocol-relative URLs and already-mounted paths
// keep their meaning. Desktop and default-root pages need no rewriting.
function magpiePath(value) {
  const base = window.bootPrefs?.basePath || "";
  if (!base || typeof value !== "string" || !value.startsWith("/") || value.startsWith("//") || value === base || value.startsWith(base + "/") || value.startsWith(base + "?")) return value;
  return base + value;
}
