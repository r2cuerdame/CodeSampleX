(() => {
  const form = document.querySelector('#report-filters');
  if (!form) return;
  const rows = document.querySelector('#report-rows');
  const status = document.querySelector('#report-status');
  const detail = document.querySelector('#report-detail');
  const prev = document.querySelector('#report-prev');
  const next = document.querySelector('#report-next');
  let offset = 0, generation = 0, detailGeneration = 0, selected = null;
  const labels = {triage:'검토 대기', 'no-replay-lane':'수동 검토 필요', 'replay-queued':'재실행 대기', resolved:'검토 완료', queued:'검증 대기', verifying:'검증 중', verified:'판정 완료', unsupported:'검증 불가'};
  const verdicts = {'confirmed-csx-defect':'제품 결함 확인', 'expected-behavior':'의도된 동작', 'client-difference':'클라이언트 차이', 'not-reproducible':'재현되지 않음', 'insufficient-evidence':'근거 부족', duplicate:'중복'};
  const el = (tag, text, cls) => { const node = document.createElement(tag); if (text !== undefined) node.textContent = text; if (cls) node.className = cls; return node; };
  const date = value => !value || value.startsWith('0001-') ? '—' : new Date(value).toLocaleString('ko-KR');
  const request = async (url, body) => {
    const response = await fetch(url, {method:body ? 'POST':'GET', credentials:'same-origin', cache:'no-store', headers:body ? {'Content-Type':'application/json','X-CSX-CSRF':'1'} : {}, body:body ? JSON.stringify(body):undefined});
    if (!response.ok) throw new Error(response.status === 409 ? '이미 판정되었습니다. 상세를 다시 열어 확인하세요.' : `${(await response.text()).slice(0,300)} (HTTP ${response.status}). 새로고침 후 다시 시도하세요.`);
    return response.json();
  };
  function clearDetail() { detailGeneration++; selected = null; detail.hidden = true; detail.replaceChildren(); }
  async function overview() {
    const node = document.querySelector('#report-overview');
    try { const {counts} = await request('/admin/api/reports?channel=all&state=open'); node.textContent = `미판정 ${counts.open}건 · 그중 자동 검증 불가 ${counts.blocked}건 · 확인된 제품 결함 중 버그 미연결 ${counts.unlinked}건`; }
    catch(error) { node.textContent = error.message; }
  }
  async function load() {
    const ticket = ++generation;
    const params = new URLSearchParams(new FormData(form)); params.set('offset',offset);
    rows.replaceChildren(); prev.disabled = next.disabled = true;
    status.textContent = '신고를 불러오는 중…';
    try {
      const page = await request('/admin/api/reports?' + params);
      if (ticket !== generation) return;
      if (offset > 0 && offset >= page.total) { offset = Math.max(0,Math.floor((page.total-1)/25)*25); return load(); }
      for (const [key,value] of Object.entries(page.counts)) {
        const node = document.querySelector(`[data-report-count="${key}"]`); if (node) node.textContent = value.toLocaleString('ko-KR');
      }
      status.textContent = page.total ? `${page.total}건 중 ${offset+1}–${offset+page.rows.length} · 최근 발생 순` : '조건에 맞는 신고가 없습니다.';
      prev.disabled = offset === 0; next.disabled = offset + page.rows.length >= page.total;
      for (const row of page.rows) {
        const tr = el('tr');
        const open = el('button',`${row.channel === 'product' ? '제품':'이상'} #${row.id}`,'report-open');
        open.type = 'button'; open.addEventListener('click',() => openDetail(row.channel,row.id));
        const idCell = el('td'); idCell.append(open); tr.append(idCell);
        const target = el('td',row.target); target.append(el('small',row.kind,'report-secondary')); tr.append(target);
        const state = el('td',labels[row.status] || row.status); state.append(el('small',row.verdict || row.reason || '판정 전','report-secondary')); tr.append(state);
        tr.append(el('td',String(row.occurrences)),el('td',date(row.lastSeen)),el('td',row.canonicalRef || '—')); rows.append(tr);
      }
    } catch(error) { if (ticket === generation) { status.textContent = error.message; document.querySelectorAll('[data-report-count]').forEach(n => n.textContent = '—'); } }
  }
  function field(parent,label,value) { const box = el('div'); box.append(el('dt',label),el('dd',value || '—')); parent.append(box); }
  async function openDetail(channel,id) {
    const ticket = ++detailGeneration;
    selected = {channel,id}; detail.hidden = false; detail.replaceChildren(el('p','상세를 불러오는 중…'));
    try {
      const data = await request(`/admin/api/reports/${channel}/${id}`);
      if (ticket !== detailGeneration) return;
      detail.replaceChildren();
      const heading = el('h2',`${channel === 'product' ? '제품 결함 신고':'이상 검증 요청'} #${id}`); heading.tabIndex = -1;
      const close = el('button','상세 닫기'); close.type = 'button'; close.onclick = clearDetail;
      const head = el('div',undefined,'section-head'); head.append(heading,close); detail.append(head);
      const metadata = el('dl',undefined,'report-metadata');
      field(metadata,'상태',labels[data.status] || data.status); field(metadata,'판정',data.verdict); field(metadata,'진행 제한 사유',data.reason);
      field(metadata,'최초 접수',date(data.firstSeen)); field(metadata,'최근 발생',date(data.lastSeen)); field(metadata,'판정 시각',date(data.verdictAt));
      field(metadata,'발생 횟수',String(data.occurrences)); field(metadata,'연결된 버그',data.canonicalRef); field(metadata,'검토 근거',data.reviewNote);
      if (channel === 'anomaly') { field(metadata,'검증 작업',data.jobId ? '#'+data.jobId:'없음'); field(metadata,'샘플',data.sampleId); }
      detail.append(metadata);
      const evidence = data.evidence || {};
      const evidenceFields = el('dl',undefined,'report-metadata');
      for (const [key,label] of [['actualBehavior','관측된 동작'],['expectedBehavior','기대 동작'],['llmHypothesis','제출자의 가설 · 미검증'],['reproducible','제출자가 보고한 재현 여부']]) if (evidence[key]) field(evidenceFields,label,evidence[key]);
      detail.append(evidenceFields);
      const raw = el('details'); raw.append(el('summary','제출된 근거 전체 · 환경 / 공개 좌표 / 관련 ID'),el('pre',JSON.stringify(data.evidence,null,2),'report-evidence')); detail.append(raw);
      if (channel === 'anomaly') detail.append(el('p','이상 신고의 판정은 독립 검증 영수증으로만 결정됩니다. 검증 불가 사유와 샘플·작업 ID를 확인하세요.','note'));
      else if (!data.verdict) renderReview(data);
      else if (data.verdict === 'confirmed-csx-defect' && !data.canonicalRef) renderLink(data);
      heading.focus();
    } catch(error) { if (ticket === detailGeneration) detail.replaceChildren(el('p',error.message)); }
  }
  function renderReview(data) {
    const review = el('form',undefined,'report-review');
    review.append(el('h3','검토 결과 기록'),el('p','근거를 확인한 뒤 한 건씩 판정합니다. 저장된 판정과 근거는 덮어쓸 수 없습니다.','note'));
    const label = el('label','판정'); const select = el('select'); select.name = 'verdict'; select.required = true;
    const placeholder = el('option','판정 선택'); placeholder.value = ''; select.append(placeholder);
    for (const [value,text] of Object.entries(verdicts)) { const option = el('option',text); option.value = value; select.append(option); } label.append(select); review.append(label);
    const noteLabel = el('label','검토 근거 · 실행 결과 또는 관련 증거'); const note = el('textarea'); note.name = 'note'; note.required = true; note.minLength = 10; note.maxLength = 1000; note.rows = 4; noteLabel.append(note); review.append(noteLabel);
    const refLabel = el('label','GitHub 이슈 참조 · 확인된 결함만'); const ref = el('input'); ref.name = 'canonicalRef'; ref.maxLength = 128; ref.placeholder = 'https://github.com/r2cuerdame/CodeSampleX/issues/…'; ref.disabled = true; refLabel.append(ref); review.append(refLabel);
    select.onchange = () => { ref.disabled = select.value !== 'confirmed-csx-defect'; if (ref.disabled) ref.value = ''; };
    const button = el('button','검토 결과 저장'); button.type = 'submit'; const message = el('p',undefined,'note'); message.setAttribute('role','status'); review.append(button,message);
    review.onsubmit = async event => {
      event.preventDefault();
      if (!window.confirm(`제품 신고 #${data.id}: ${verdicts[select.value]} 판정과 근거를 저장할까요? 이 판정은 변경할 수 없습니다.`)) return;
      button.disabled = true;
      try { await request('/admin/api/reports/review',{id:data.id,verdict:select.value,note:note.value,canonicalRef:ref.value}); await load(); overview(); if (selected?.id === data.id && selected.channel === 'product') await openDetail('product',data.id); }
      catch(error) { message.textContent = error.message; button.disabled = false; }
    };
    detail.append(review);
  }
  function renderLink(data) {
    const link = el('form',undefined,'report-review'); const label = el('label','확인된 결함의 GitHub 이슈 연결'); const input = el('input'); input.required = true; input.maxLength = 128; label.append(input);
    const button = el('button','버그 참조 연결'); button.type = 'submit'; const message = el('p'); message.setAttribute('role','status'); link.append(label,button,message);
    link.onsubmit = async event => { event.preventDefault(); button.disabled = true; try { const result = await request('/admin/api/csx-issues/canonical',{id:data.id,ref:input.value}); if (!result.linked) throw new Error('연결되지 않았습니다. 상세를 다시 확인하세요.'); await load(); await openDetail('product',data.id); } catch(error) { message.textContent = error.message; button.disabled = false; } }; detail.append(link);
  }
  form.onsubmit = event => { event.preventDefault(); offset = 0; clearDetail(); load(); };
  form.querySelectorAll('select').forEach(node => node.onchange = () => { offset = 0; clearDetail(); load(); });
  prev.onclick = () => { offset = Math.max(0,offset-25); clearDetail(); load(); };
  next.onclick = () => { offset += 25; clearDetail(); load(); };
  document.querySelector('#report-refresh').onclick = () => { clearDetail(); load(); };
  load();
  overview();
})();
