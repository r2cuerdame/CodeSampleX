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

  const download = (text, filename) => {
    const type = filename.endsWith(".sh") ? "text/x-shellscript;charset=utf-8" : "application/x-msdos-program;charset=us-ascii";
    const blob = new Blob([text], {type});
    const url = URL.createObjectURL(blob);
    const link = document.createElement("a");
    link.href = url;
    link.download = filename;
    link.hidden = true;
    document.body.appendChild(link);
    link.click();
    link.remove();
    window.setTimeout(() => URL.revokeObjectURL(url), 0);
  };

  const safeFilename = (value) => value.replace(/[^a-z0-9._-]+/gi, "-").replace(/^-+|-+$/g, "") || "worker";

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
        const actions = document.createElement("div");
        actions.className = "sample-worker-actions";
        const recopy = document.createElement("button");
        recopy.type = "button";
        recopy.className = "copy-button";
        recopy.textContent = "프롬프트 + CLI 재복사";
        const downloadCMD = document.createElement("button");
        downloadCMD.type = "button";
        downloadCMD.className = "copy-button";
        downloadCMD.textContent = "무한 CMD 내려받기";
		const downloadLinux = document.createElement("button");
		downloadLinux.type = "button";
		downloadLinux.className = "copy-button";
		downloadLinux.textContent = "무한 Linux SH 내려받기";
        const rotateAndDeliver = async (deliver, progress, success, failure) => {
          recopy.disabled = true;
          downloadCMD.disabled = true;
		  downloadLinux.disabled = true;
          status.textContent = progress;
          try {
            const data = await request(`/admin/api/authoring-sessions/${encodeURIComponent(session.sessionId)}/rotate`, {method: "POST", body: "{}"});
            await deliver(data);
            status.textContent = success;
            await load();
          } catch (_) {
            status.textContent = failure;
            recopy.disabled = false;
            downloadCMD.disabled = false;
			downloadLinux.disabled = false;
          }
        };
        recopy.addEventListener("click", () => rotateAndDeliver(
          (data) => copy(data.prompt),
          `${session.label} 명령을 새 토큰으로 교체하는 중…`,
          `${session.label}의 새 프롬프트와 CLI를 복사했습니다. 이전 명령은 무효입니다.`,
          "재복사에 실패했습니다. 다시 누르면 새 명령으로 교체됩니다.",
        ));
        if (session.model === "agy") {
          downloadCMD.addEventListener("click", () => rotateAndDeliver(
            (data) => {
              if (!data.worker.windowsCmd) throw new Error("CMD unavailable");
              download(data.worker.windowsCmd, `codesamplex-${safeFilename(session.label)}.cmd`);
            },
            `${session.label} 무한 CMD를 새 토큰으로 만드는 중…`,
            `${session.label}의 CMD를 내려받았습니다. 이전 프롬프트와 명령은 무효입니다. CMD 파일을 실행하면 별도 창에서 계속 재조회합니다.`,
            "CMD 생성에 실패했습니다. 다시 누르면 새 토큰으로 교체됩니다.",
          ));
		  downloadLinux.addEventListener("click", () => rotateAndDeliver(
			(data) => {
			  if (!data.worker.linuxSh) throw new Error("Linux SH unavailable");
			  download(data.worker.linuxSh, `codesamplex-${safeFilename(session.label)}.sh`);
			},
			`${session.label} 무한 Linux SH를 새 토큰으로 만드는 중…`,
			`${session.label}의 Linux SH를 내려받았습니다. 이전 프롬프트와 명령은 무효입니다. WSL/Linux에서 bash로 실행하세요.`,
			"Linux SH 생성에 실패했습니다. 다시 누르면 새 토큰으로 교체됩니다.",
		  ));
        }
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
        actions.append(recopy);
		if (session.model === "agy") actions.append(downloadCMD, downloadLinux);
        actions.append(revoke);
        row.append(info, actions);
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
    if (!model) return;
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
	  status.textContent = `${data.workers.length}개 샘플 워커의 작업 프롬프트와 완성된 CLI 갱신 명령을 복사했습니다. AGY 세션은 각 행에서 무한 CMD와 Linux SH도 내려받을 수 있습니다.`;
      await load();
    } catch (_) {
      status.textContent = "발급 또는 복사에 실패했습니다.";
    } finally {
      button.disabled = false;
    }
  });

  load();
})();

(() => {
  const form = document.querySelector("#admin-token-form");
  const list = document.querySelector("#admin-token-list");
  const status = document.querySelector("#admin-token-status");
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

  const load = async () => {
    try {
      const data = await request("/admin/api/admin-tokens");
      list.replaceChildren();
      const live = data.tokens.filter((token) => !token.revoked);
      if (!live.length) {
        const empty = document.createElement("p");
        empty.className = "empty";
        empty.textContent = "발급된 운영 토큰이 없습니다.";
        list.appendChild(empty);
        return;
      }
      for (const token of live) {
        const row = document.createElement("div");
        row.className = "sample-worker-row";
        const info = document.createElement("div");
        const name = document.createElement("strong");
        name.textContent = token.label;
        const meta = document.createElement("span");
        meta.className = "note";
        // A token with no expiry has nothing to watch but its use, so the
        // last-used stamp leads rather than trails.
        const used = token.lastUsedAt
          ? `마지막 사용 ${formatTime(token.lastUsedAt)} · ${token.lastUsedIp || "IP 확인 안 됨"}`
          : "아직 사용된 적 없음";
        const life = token.expiresAt ? `만료 ${formatTime(token.expiresAt)}` : "무제한";
        meta.textContent = `${life} · ${used} · 발급 ${formatTime(token.issuedAt)}`;
        info.append(name, meta);
        const actions = document.createElement("div");
        actions.className = "sample-worker-actions";
        const revoke = document.createElement("button");
        revoke.type = "button";
        revoke.className = "danger-button";
        revoke.textContent = "폐기";
        revoke.addEventListener("click", async () => {
          revoke.disabled = true;
          try {
            await request(`/admin/api/admin-tokens/${encodeURIComponent(token.tokenId)}`, {method: "DELETE", body: "{}"});
            status.textContent = `${token.label} 토큰을 폐기했습니다. 이 토큰을 쓰던 머신은 즉시 인증에 실패합니다.`;
            await load();
          } catch (_) {
            status.textContent = "토큰을 폐기하지 못했습니다.";
            revoke.disabled = false;
          }
        });
        actions.append(revoke);
        row.append(info, actions);
        list.appendChild(row);
      }
    } catch (_) {
      status.textContent = "운영 토큰 목록을 불러오지 못했습니다.";
    }
  };

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = form.querySelector("button[type=submit]");
    const label = form.elements.label.value.trim();
    const count = Number(form.elements.count.value);
    const unlimited = form.elements.unlimited.checked;
    const ttlDays = Number(form.elements.ttlDays.value);
    if (!label) return;
    button.disabled = true;
    status.textContent = "운영 토큰을 발급하는 중…";
    try {
      const body = unlimited ? {label, count, unlimited: true} : {label, count, ttlDays};
      const data = await request("/admin/api/admin-tokens", {method: "POST", body: JSON.stringify(body)});
      // This is the only moment the plaintext exists outside the caller's
      // hands, so it goes to the clipboard here or not at all.
      await copy(data.tokens.map((token) => `${token.label}\t${token.token}`).join("\n"));
      status.textContent = `${data.tokens.length}개를 복사했습니다. 지금 붙여넣어 두세요 — 평문은 다시 볼 수 없습니다.`;
      await load();
    } catch (_) {
      status.textContent = "토큰을 발급하지 못했습니다. 유효기간을 지정했는지, 무제한을 골랐는지 확인해 주세요.";
    } finally {
      button.disabled = false;
    }
  });

  load();
})();

(() => {
  const list = document.querySelector("#farm-workers");
  const health = document.querySelector("#farm-health");
  const cost = document.querySelector("#farm-cost");
  if (!list || !health || !cost) return;

  const num = (n) => n.toLocaleString("ko-KR");
  const since = (iso) => {
    if (!iso) return null;
    const mins = Math.round((Date.now() - new Date(iso).getTime()) / 60000);
    return mins < 1 ? "방금" : mins < 60 ? `${mins}분 전` : `${Math.round(mins / 60)}시간 전`;
  };

  const stat = (label, value, warn) => {
    const box = document.createElement("div");
    box.className = "stat";
    const l = document.createElement("span");
    l.className = "label";
    l.textContent = label;
    const v = document.createElement("strong");
    v.textContent = value;
    if (warn) v.className = "bad";
    box.append(l, v);
    return box;
  };

  const load = async () => {
    let data;
    try {
      const response = await fetch("/admin/api/farm", {
        headers: {Accept: "application/json"}, credentials: "same-origin", cache: "no-store",
      });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      data = await response.json();
    } catch (_) {
      // Say nothing rather than zeros: "not measured" and "nothing wrong" must
      // not look the same on this panel.
      list.replaceChildren();
      const p = document.createElement("p");
      p.className = "empty";
      p.textContent = "팜 지표를 불러오지 못했습니다.";
      list.appendChild(p);
      return;
    }

    list.replaceChildren();
    if (!data.workers.length) {
      const p = document.createElement("p");
      p.className = "empty";
      p.textContent = "활성 워커가 없습니다.";
      list.appendChild(p);
    }
    for (const worker of data.workers) {
      const row = document.createElement("div");
      row.className = "sample-worker-row";
      const info = document.createElement("div");
      const name = document.createElement("strong");
      name.textContent = worker.computerName || worker.label;
      const meta = document.createElement("span");
      meta.className = "note";
      if (!worker.started) {
        name.classList.add("bad");
        meta.textContent = `세션 발급 ${since(worker.issuedAt)} · 아직 갱신 없음 · 초안 0건 — 워커가 뜨지 못했습니다`;
      } else {
        const rate = worker.perHour ? `${worker.perHour.toFixed(1)}건/시간` : "—";
        const holding = worker.holding ? ` · 작업 중 ${worker.holding}` : "";
        meta.textContent = `${rate} · 최근 1시간 ${worker.drafts}건(공개 ${worker.published}) · 갱신 ${since(worker.lastRefreshAt)}${holding}`;
      }
      info.append(name, meta);
      row.append(info);
      list.appendChild(row);
    }

    health.replaceChildren();
    const h = data.health;
    const dupRate = h.duplicateRate === undefined ? "—" : `${(h.duplicateRate * 100).toFixed(1)}%`;
    health.append(
      stat("공개 샘플", num(h.publicSamples)),
      stat("중복 좌표", `${num(h.duplicateCoordinates)} · ${dupRate}`, h.duplicateCoordinates > 0),
      stat("잠긴 좌표", num(h.staleClaims), h.staleClaims > 0),
      stat("OS 커버리지", Object.entries(h.receiptsByOs || {})
        .map(([os, n]) => `${os} ${num(n)}`).join(" · ") || "—"),
    );

    cost.textContent = data.instances.length
      ? `${data.instances.map((i) => `${i.name} $${i.monthlyUsd}`).join(" · ")} — 합계 $${data.monthlyTotalUsd}/월`
      : "인스턴스 비용이 설정되지 않았습니다 (CSX_INSTANCES).";
  };

  load();
  window.setInterval(load, 60000);
})();
