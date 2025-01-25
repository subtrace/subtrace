const subtrace = require("subtrace");
const http = require("http");

http.createServer((req, res) => {
  res.write(req.method + " " + req.url + "\n");
  res.end();
}).listen(8080, () => console.log("listening on port 8080..."));
