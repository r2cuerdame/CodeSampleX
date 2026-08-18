"use strict";

(() => {
  const form = document.querySelector("#sample-worker-form");
  const list = document.querySelector("#sample-worker-sessions");
  const status = document.querySelector("#sample-worker-status");
  if (!form || !list || !status) return;

  const request = async (url, options = {}) => {
    const headers = new Headers(options.headers || {});
    headers.set("Accept", "application/json");
    if (options.method && options.method !== "GET") {
      headers.set("Content-Type", "application/json");
      headers.set("X-CSX-CSRF", "1");
    }
    const response = await fetch(url, {...options, headers, credentials: "same-origin", cache: "no-store"});
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
    return response.status === 204 ? null : response.json();
  };

  const copy = async (text) => {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(text);
      return;
    }
    const area = document.createElement("textarea");
    area.value = text;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.opacity = "0";
    document.body.appendChild(area);
    area.select();
    const ok = document.execCommand("copy");
    area.remove();
    if (!ok) throw new Error("copy failed");
  };

  const formatTime = (value) => value ? new Date(value).toLocaleString("ko-KR") : "—";

  const revokeIssued = async (workers) => {
    await Promise.allSettled(workers.map((worker) => request(
      `/admin/api/authoring-sessions/${encodeURIComponent(worker.session.sessionId)}`,
      {method: "DELETE", body: "{}"},
    )));
  };

  const load = async () => {
    try {
      const data = await request("/admin/api/authoring-sessions");
      list.replaceChildren();
      if (!data.sessions.length) {
        const empty = document.createElement("p");
        empty.className = "empty";
        empty.textContent = "활성 샘플 워커가 없습니다.";
        list.appendChild(empty);
        return;
      }
      for (const session of data.sessions) {
        const row = document.createElement("div");
        row.className = "sample-worker-row";
        const info = document.createElement("div");
        const name = document.createElement("strong");
        name.textContent = session.label;
        const meta = document.createElement("span");
        meta.className = "note";
        meta.textContent = `${session.model} · 추론 ${session.reasoning} · 컴퓨터 ${session.computerName || "확인 안 됨"} · IP ${session.lastIp || "확인 안 됨"} · 마지막 갱신 ${formatTime(session.lastRefreshedAt)} · 만료 ${formatTime(session.idleExpiresAt)}`;
        info.append(name, meta);
        const revoke = document.createElement("button");
        revoke.type = "button";
        revoke.className = "danger-button";
        revoke.textContent = "취소";
        revoke.addEventListener("click", async () => {
          revoke.disabled = true;
          try {
            await request(`/admin/api/authoring-sessions/${encodeURIComponent(session.sessionId)}`, {method: "DELETE", body: "{}"});
            status.textContent = `${session.label} 세션을 취소했습니다.`;
            await load();
          } catch (_) {
            status.textContent = "세션을 취소하지 못했습니다.";
            revoke.disabled = false;
          }
        });
        row.append(info, revoke);
        list.appendChild(row);
      }
    } catch (_) {
      status.textContent = "샘플 워커 목록을 불러오지 못했습니다.";
    }
  };

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = form.querySelector("button[type=submit]");
    const label = form.elements.label.value.trim();
    const model = form.elements.model.value.trim();
    const reasoning = form.elements.reasoning.value;
    const count = Number(form.elements.count.value);
    if (!label) return;
    button.disabled = true;
    status.textContent = "샘플 워커 세션을 발급하는 중…";
    let data;
    try {
      data = await request("/admin/api/authoring-sessions", {method: "POST", body: JSON.stringify({label, model, reasoning, count})});
      try {
        await copy(data.prompt);
      } catch (error) {
        await revokeIssued(data.workers || []);
        await load();
        throw error;
      }
      status.textContent = `${data.workers.length}개 샘플 워커의 작업 프롬프트와 완성된 CLI 갱신 명령을 복사했습니다.`;
      await load();
    } catch (_) {
      status.textContent = "발급 또는 복사에 실패했습니다.";
    } finally {
      button.disabled = false;
    }
  });

  load();
})();
