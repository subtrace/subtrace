import "subtrace";

import express from "express";

const app = express();

app.get("/", (req, res) => {
  console.log(`${req.method} ${req.url}`);

  res.send("Hello, World!");
});

const PORT = 3000;
app.listen(PORT, () => {
  console.log(`Server is running on http://localhost:${PORT}`);
});
