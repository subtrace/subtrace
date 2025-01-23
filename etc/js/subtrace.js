import * as Module from "module";
import * as OS from "os";
import * as Path from "path";
import * as Process from "process";

// ESM doesn't allow importing .node files, see this issue:
// https://github.com/nodejs/node/issues/40541
const require = Module.createRequire(import.meta.url);
const execve = require("./build/Release/execve.node");

function reExecWithSubtrace() {
  if (Process.env["SUBTRACE_RUN"] === "1") {
    // Already running in Subtrace
    return;
  }

  let os = OS.platform().trim().toLocaleLowerCase();
  let arch = OS.arch().trim().toLocaleLowerCase();

  if (arch === "aarch64") {
    arch = "arm64";
  } else if (arch === "x86_64" || arch === "x64") {
    arch = "amd64";
  }

  const subtraceExecutableName = Path.join(
    import.meta.dirname,
    `./subtrace/subtrace-${os}-${arch}`
  );

  execve.execve(
    subtraceExecutableName,
    [subtraceExecutableName, "run", "-log=true", "--", ...Process.argv],
    []
  );
}

if (OS.arch().trim().toLocaleLowerCase() === "linux") {
  reExecWithSubtrace();
}

