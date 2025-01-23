import "subtrace";

import { createServer } from "http";

const PORT = 3000;
const HOSTNAME = "127.0.0.1";

const server = createServer((req, res) => {
  console.log(`${req.method} ${req.url}`);

  res.statusCode = 200;
  res.setHeader("Content-Type", "text/plain");

  res.end("Hello, World!\n");
});

server.listen(PORT, HOSTNAME, () => {
  console.log(`Server running at http://${HOSTNAME}:${PORT}/`);
});
