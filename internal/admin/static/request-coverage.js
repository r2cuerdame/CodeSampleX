(() => {
  "use strict";

  const panel = document.getElementById("request-coverage");
  if (!panel) return;

  const rows = [...panel.querySelectorAll(".coverage-missing-row")];
  const nodes = [...panel.querySelectorAll("button.coverage-node")];
  const packages = [...panel.querySelectorAll(".coverage-package-filter")];
  const reset = panel.querySelector("#coverage-filter-reset");
  const status = panel.querySelector("#coverage-filter-status");
  const graph = panel.querySelector("#coverage-gap-graph");

  const setGraph = (node) => {
    if (!graph || !node) return;
    graph.hidden = false;
    const values = {
      package: node.dataset.packageLabel,
      version: node.dataset.versionLabel,
      symbol: node.dataset.symbolLabel,
      environment: node.dataset.environmentLabel,
      boundary: node.dataset.boundaryLabel,
      failure: node.dataset.failureLabel,
      impact: node.dataset.impact,
    };
    Object.entries(values).forEach(([name, value]) => {
      const target = graph.querySelector(`[data-graph-value="${name}"]`);
      if (target) target.textContent = value || "—";
    });
  };

  const apply = (packageKey, coordinateKey, label, node) => {
    let shown = 0;
    rows.forEach((row) => {
      const visible = coordinateKey
        ? row.dataset.coordinate === coordinateKey
        : !packageKey || row.dataset.package === packageKey;
      row.hidden = !visible;
      if (visible) shown += 1;
    });
    nodes.forEach((item) => item.setAttribute("aria-pressed",
      coordinateKey && item.dataset.coordinate === coordinateKey ? "true" : "false"));
    packages.forEach((item) => item.setAttribute("aria-pressed",
      !coordinateKey && packageKey && item.dataset.package === packageKey ? "true" : "false"));
    if (status) status.textContent = `${label} · Top Missing Areas ${shown}개 표시`;
    if (reset) reset.hidden = !packageKey && !coordinateKey;
    if (node) setGraph(node);
    else if (graph) graph.hidden = true;
  };

  packages.forEach((button) => button.addEventListener("click", () => {
    apply(button.dataset.package, "", button.dataset.packageLabel, null);
  }));
  nodes.forEach((button) => button.addEventListener("click", () => {
    apply(button.dataset.package, button.dataset.coordinate,
      `${button.dataset.packageLabel} · ${button.dataset.versionLabel} · ${button.dataset.symbolLabel}`, button);
  }));
  if (reset) reset.addEventListener("click", () => {
    apply("", "", "전체 최근 수요", null);
    if (graph) graph.hidden = true;
  });
})();
