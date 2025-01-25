let counter = 0;

(function() {
  const count = counter++;
  if (count == 0) {
    require("./build/Release/subtrace.node").init(__dirname);
  }
})();
