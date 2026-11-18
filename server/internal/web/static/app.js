(() => {
  const state = {
    providers: [],
    entriesConfig: new Map(),
    snapshot: null,
    discover: [],
    filter: 'all',
    showRemaining: localStorage.getItem('subbar_mode') !== 'used',
    theme: localStorage.getItem('subbar_theme') || 'dark',
    editingId: null,
    oauthSession: null,
    oauthPollTimer: null,
  };

  const $ = (id) => document.getElementById(id);

  // Symbol names are local constants; provider/account labels remain plain text nodes.
  function icon(name) {
    const ns = 'http://www.w3.org/2000/svg';
    const svg = document.createElementNS(ns, 'svg');
    svg.setAttribute('class', 'icon');
    svg.setAttribute('aria-hidden', 'true');
    svg.setAttribute('focusable', 'false');
    const use = document.createElementNS(ns, 'use');
    use.setAttribute('href', `/material-icons.svg#${name}`);
    svg.appendChild(use);
    return svg;
  }

  function cardAction(symbol, label) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'mini-btn';
    btn.title = label;
    btn.setAttribute('aria-label', label);
    btn.appendChild(icon(symbol));
    return btn;
  }

  function applyTheme(t) {
    state.theme = t === 'light' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', state.theme);
    localStorage.setItem('subbar_theme', state.theme);
    const btn = $('btn-theme');
    btn.title = state.theme === 'dark' ? '切换到浅色主题' : '切换到深色主题';
    btn.setAttribute('aria-label', btn.title);
    btn.replaceChildren(icon(state.theme === 'dark' ? 'light_mode' : 'dark_mode'));
  }

  function applyModeButton() {
    $('btn-mode').textContent = state.showRemaining ? '看剩余%' : '看已用%';
  }

  function showToast(msg) {
    const el = $('toast');
    el.textContent = msg;
    el.classList.remove('hidden');
    clearTimeout(showToast._timer);
    showToast._timer = setTimeout(() => el.classList.add('hidden'), 2400);
  }

  async function api(path, opts = {}) {
    const res = await fetch(path, {
      headers: { 'Content-Type': 'application/json', ...(opts.headers || {}) },
      ...opts,
    });
    const text = await res.text();
    let data = null;
    try { data = text ? JSON.parse(text) : null; } catch (_) {}
    if (!res.ok) {
      throw new Error((data && data.error) || `请求失败 (${res.status})`);
    }
    return data;
  }

  async function loadAll() {
    try {
      const [providers, entries, snapshot, discover] = await Promise.all([
        api('/api/providers'),
        api('/api/entries'),
        api('/api/snapshot'),
        api('/api/discover').catch(() => ({ hints: [] })),
      ]);
      state.providers = providers.providers || [];
      state.entriesConfig.clear();
      for (const e of (entries.entries || [])) {
        state.entriesConfig.set(e.id, e);
      }
      state.snapshot = snapshot;
      state.discover = (discover && discover.hints) || [];
      renderAll();
      $('btn-add').disabled = false;
      $('load-error').classList.add('hidden');
      return true;
    } catch (err) {
      if (!state.snapshot) $('updated-text').textContent = '尚未同步';
      const message = state.snapshot ? '同步失败，仍显示上次的数据' : '无法加载监控入口';
      $('load-error-text').textContent = `${message}：${err.message || '请检查服务连接'}`;
      $('load-error').classList.remove('hidden');
      return false;
    }
  }

  function formatCountdown(iso) {
    if (!iso) return '';
    const diffSec = Math.floor((new Date(iso).getTime() - Date.now()) / 1000);
    if (diffSec <= 0) return '即将重置';
    const d = Math.floor(diffSec / 86400);
    const h = Math.floor((diffSec % 86400) / 3600);
    const m = Math.floor((diffSec % 3600) / 60);
    if (d > 0) return `${d}天 ${h}小时后重置`;
    if (h > 0) return `${h}小时 ${m}分后重置`;
    return `${Math.max(1, m)}分钟后重置`;
  }

  function formatRelativeAgo(iso) {
    if (!iso) return '未刷新';
    const sec = Math.max(0, Math.floor((Date.now() - new Date(iso).getTime()) / 1000));
    if (sec < 10) return '刚刚更新';
    if (sec < 60) return `${sec}秒前`;
    const m = Math.floor(sec / 60);
    if (m < 60) return `${m}分钟前`;
    const h = Math.floor(m / 60);
    return `${h}小时前`;
  }

  function formatCurrencyTotals(arr) {
    if (!arr || arr.length === 0) return '暂无';
    return arr.map((x) => `${x.symbol}${Number(x.amount).toFixed(2)}`).join(' · ');
  }

  function statusBadgeLabel(st, reason) {
    switch (st) {
      case 'ok': return '正常';
      case 'warning': return reason || '余量偏低';
      case 'critical': return reason || '即将耗尽';
      case 'stale': return '缓存快照';
      case 'error': return '拉取异常';
      case 'disabled': return '已停用';
      default: return st;
    }
  }

  function hasQuotaOrBalance(item) {
    return Boolean((item.windows && item.windows.length > 0) || item.balance != null);
  }

  function isInitialErrorEntry(item) {
    return !hasQuotaOrBalance(item) && item.status === 'error';
  }

  function renderAll() {
    if (!state.snapshot) return;
    const { summary, entries, generatedAt } = state.snapshot;
    const enabled = entries.filter((e) => e.enabled);
    const initialErrorCount = enabled.filter(isInitialErrorEntry).length;
    const enabledVisibleCount = enabled.length - initialErrorCount;

    $('updated-text').textContent = `最近同步 ${new Date(generatedAt).toLocaleTimeString('zh-CN', { hour12: false })}`;
    $('stat-balances').textContent = formatCurrencyTotals(summary.availableBalances);
    if (summary.criticalCount > 0) {
      $('stat-health').textContent = `${summary.criticalCount} 告警 / 共 ${enabledVisibleCount} 项`;
    } else if (summary.warningCount > 0) {
      $('stat-health').textContent = `${summary.warningCount} 预警 / 共 ${enabledVisibleCount} 项`;
    } else {
      $('stat-health').textContent = enabledVisibleCount ? `${enabledVisibleCount} 项正常` : '暂无可用监控数据';
    }
    if (initialErrorCount) {
      const pending = `${initialErrorCount} 项首次同步失败`;
      $('stat-health').textContent = enabledVisibleCount ? `${$('stat-health').textContent} · ${pending}` : pending;
    }
    $('stat-monthly').textContent = summary.monthlyFixedCosts && summary.monthlyFixedCosts.length > 0
      ? formatCurrencyTotals(summary.monthlyFixedCosts) + ' / 月'
      : '未设月费';

    $('cnt-all').textContent = entries.length;
    $('cnt-sub').textContent = entries.filter((e) => e.kind === 'subscription').length;
    $('cnt-bal').textContent = entries.filter((e) => e.kind === 'balance').length;

    renderDiscover(entries);
    renderCards(entries);
  }

  function renderDiscover(entries) {
    const box = $('discover-box');
    const existingProviders = new Set(entries.map((e) => e.provider));
    const pending = state.discover.filter((h) => !existingProviders.has(h.provider));
    if (pending.length === 0) {
      box.classList.add('hidden');
      box.innerHTML = '';
      return;
    }
    box.classList.remove('hidden');
    box.innerHTML = '';
    const title = document.createElement('div');
    title.className = 'discover-title';
    title.textContent = '检测到本机已登录账号（可免浏览器一键同步，也可点右上角通过授权链接接入任意账号）：';
    const row = document.createElement('div');
    row.className = 'discover-items';
    for (const h of pending) {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = 'btn';
      btn.append(icon('sync'), `一键同步 ${h.suggestedName}`);
      btn.addEventListener('click', async () => {
        btn.disabled = true;
        try {
          await api('/api/discover/import', {
            method: 'POST',
            body: JSON.stringify({
              hintId: h.id,
              pinned: entries.filter((e) => !isInitialErrorEntry(e) && e.pinned).length < 2,
            }),
          });
          showToast(`已同步接入 ${h.suggestedName}`);
          await loadAll();
        } catch (err) {
          showToast(err.message);
        } finally {
          btn.disabled = false;
        }
      });
      row.appendChild(btn);
    }
    box.appendChild(title);
    box.appendChild(row);
  }

  function renderCards(entries) {
    const container = $('cards-container');
    const drawerWasOpen = container.querySelector('.initial-errors-drawer')?.open;
    container.innerHTML = '';
    const visible = entries.filter((e) => !isInitialErrorEntry(e));
    const initialErrors = entries.filter((e) => isInitialErrorEntry(e) && (state.filter === 'all' || e.kind === state.filter));
    const filtered = visible.filter((e) => state.filter === 'all' || e.kind === state.filter);

    if (filtered.length === 0 && initialErrors.length === 0) {
      const empty = document.createElement('div');
      empty.className = 'empty-state';
      const isFiltered = entries.length > 0;
      const kind = state.filter === 'balance' ? 'API 余额' : '订阅配额';
      const heading = document.createElement('h2');
      heading.textContent = isFiltered ? `还没有${kind}入口` : '还没有监控入口';
      const hint = document.createElement('p');
      hint.textContent = isFiltered ? '其他分类中还有入口，可以返回查看。' : '添加一个订阅或 API 余额入口，就能在这里查看用量和刷新状态。';
      const action = document.createElement('button');
      action.type = 'button';
      action.className = 'btn btn-primary';
      if (isFiltered) {
        action.textContent = '查看全部入口';
        action.addEventListener('click', () => {
          const all = document.querySelector('[data-filter="all"]');
          all.click();
          all.focus();
        });
      } else {
        action.append(icon('add'), '添加第一个入口');
        action.addEventListener('click', () => openEntryModal(null));
      }
      empty.append(heading, hint, action);
      container.appendChild(empty);
      return;
    }

    for (const item of filtered) {
      container.appendChild(buildCard(item));
    }

    if (initialErrors.length > 0) {
      const details = document.createElement('details');
      details.className = 'initial-errors-drawer';
      details.open = !!drawerWasOpen;
      const sum = document.createElement('summary');
      sum.textContent = `${initialErrors.length} 个入口尚未取得额度或余额（可重试或编辑）`;
      details.appendChild(sum);
      const inner = document.createElement('div');
      inner.className = 'initial-errors-grid';
      for (const item of initialErrors) {
        inner.appendChild(buildCard(item));
      }
      details.appendChild(inner);
      container.appendChild(details);
    }
  }

  function buildCard(item) {
    const card = document.createElement('article');
    card.className = `entry-card status-${item.status}`;

    // Header
    const head = document.createElement('div');
    head.className = 'card-head';

    const left = document.createElement('div');
    left.className = 'card-title-group';
    const badge = document.createElement('span');
    badge.className = 'badge-pill';
    badge.textContent = item.badge || 'SUB';

    const stack = document.createElement('div');
    stack.className = 'title-stack';
    const nameRow = document.createElement('div');
    nameRow.className = 'entry-name-row';

    const h3 = document.createElement('h3');
    h3.className = 'entry-name';
    h3.textContent = item.name;
    nameRow.appendChild(h3);

    if (item.planLabel) {
      const plan = document.createElement('span');
      plan.className = 'plan-tag';
      plan.textContent = item.planLabel;
      nameRow.appendChild(plan);
    }
    if (item.pinned) {
      const pin = document.createElement('span');
      pin.className = 'pin-tag';
      pin.textContent = '状态栏置顶';
      nameRow.appendChild(pin);
    }
    const stChip = document.createElement('span');
    stChip.className = `status-chip ${item.status}`;
    stChip.textContent = statusBadgeLabel(item.status, item.statusReason);
    nameRow.appendChild(stChip);

    const sub = document.createElement('div');
    sub.className = 'provider-sub';
    sub.textContent = item.providerLabel || item.provider;
    if (item.accountEmail) {
      const acct = document.createElement('div');
      acct.className = 'account-line';
      acct.textContent = item.accountEmail;
      acct.title = item.accountEmail;
      sub.appendChild(acct);
    }

    stack.appendChild(nameRow);
    stack.appendChild(sub);
    left.appendChild(badge);
    left.appendChild(stack);

    const actions = document.createElement('div');
    actions.className = 'card-actions';

    if (item.provider === 'manual') {
      const qBtn = cardAction('tune', `快捷调整额度/余额 · ${item.name}`);
      qBtn.addEventListener('click', () => openQuickModal(item));
      actions.appendChild(qBtn);
    }

    const rBtn = cardAction('refresh', `单独刷新此项 · ${item.name}`);
    rBtn.addEventListener('click', async () => {
      rBtn.disabled = true;
      try {
        await api(`/api/entries/${encodeURIComponent(item.id)}/refresh`, { method: 'POST' });
        if (await loadAll()) showToast(`已刷新 ${item.name}`);
      } catch (err) {
        showToast(err.message);
      } finally {
        rBtn.disabled = false;
      }
    });

    const eBtn = cardAction('edit', `编辑入口配置 · ${item.name}`);
    eBtn.addEventListener('click', () => openEntryModal(item.id));

    actions.appendChild(rBtn);
    actions.appendChild(eBtn);

    head.appendChild(left);
    head.appendChild(actions);
    card.appendChild(head);

    // Windows
    if (item.windows && item.windows.length > 0) {
      const wList = document.createElement('div');
      wList.className = 'windows-list';
      for (const w of item.windows) {
        const wEl = document.createElement('div');
        wEl.className = 'win-item';

        const pctVal = state.showRemaining ? w.remainingPercent : w.usedPercent;
        const pctLabel = w.key === 'cycle'
          ? (w.usedDisplay || `已过 ${w.usedPercent.toFixed(0)}%`)
          : (state.showRemaining ? `剩余 ${pctVal.toFixed(0)}%` : `已用 ${pctVal.toFixed(0)}%`);

        const top = document.createElement('div');
        top.className = 'win-top';
        const lbl = document.createElement('span');
        lbl.className = 'win-label';
        lbl.textContent = w.label;
        const met = document.createElement('span');
        met.className = 'win-metric';
        met.textContent = w.usedDisplay && w.key === 'quota'
          ? `${w.usedDisplay} (${pctLabel})`
          : pctLabel;
        top.appendChild(lbl);
        top.appendChild(met);

        const track = document.createElement('div');
        track.className = 'bar-track';
        const fill = document.createElement('div');
        const barWidth = state.showRemaining && w.key !== 'cycle' ? w.remainingPercent : w.usedPercent;
        let tone = '';
        if (w.key !== 'cycle') {
          if (w.usedPercent >= 90) tone = 'crit';
          else if (w.usedPercent >= 75) tone = 'warn';
        }
        fill.className = `bar-fill ${tone}`;
        fill.style.width = `${Math.max(2, Math.min(100, barWidth))}%`;
        track.appendChild(fill);

        if (typeof w.paceDeltaPercent === 'number' && w.windowSeconds > 0 && w.resetsAt) {
          const elapsedPct = Math.max(0, Math.min(100, w.usedPercent - w.paceDeltaPercent));
          const markerPos = state.showRemaining ? (100 - elapsedPct) : elapsedPct;
          const marker = document.createElement('div');
          marker.className = 'pace-marker';
          marker.title = `时间进度基准线 (${elapsedPct.toFixed(0)}%)`;
          marker.style.left = `${markerPos}%`;
          track.appendChild(marker);
        }

        const foot = document.createElement('div');
        foot.className = 'win-foot';
        const cd = document.createElement('span');
        cd.className = 'countdown-span';
        cd.dataset.reset = w.resetsAt || '';
        cd.textContent = formatCountdown(w.resetsAt);
        foot.appendChild(cd);

        if (typeof w.paceDeltaPercent === 'number') {
          const pace = document.createElement('span');
          if (w.paceDeltaPercent > 8) {
            pace.className = 'pace-fast';
            pace.textContent = `消耗超前 +${w.paceDeltaPercent.toFixed(0)}%`;
          } else {
            pace.className = 'pace-ok';
            pace.textContent = `节奏稳健`;
          }
          foot.appendChild(pace);
        }

        wEl.appendChild(top);
        wEl.appendChild(track);
        wEl.appendChild(foot);
        wList.appendChild(wEl);
      }
      card.appendChild(wList);
    }

    // Balance
    if (item.balance) {
      const bBlock = document.createElement('div');
      bBlock.className = 'balance-block';

      const bMain = document.createElement('div');
      bMain.className = 'balance-main';
      const fig = document.createElement('div');
      fig.className = 'balance-figure';
      fig.textContent = `${item.balance.symbol}${Number(item.balance.available).toFixed(2)}`;
      bMain.appendChild(fig);

      if (item.balance.limit && item.balance.limit > 0) {
        const cap = document.createElement('span');
        cap.className = 'win-label';
        cap.textContent = `总额 ${item.balance.symbol}${Number(item.balance.limit).toFixed(2)}`;
        bMain.appendChild(cap);
      }
      bBlock.appendChild(bMain);

      if (item.balance.details && item.balance.details.length > 0) {
        const chips = document.createElement('div');
        chips.className = 'detail-chips';
        for (const d of item.balance.details) {
          const c = document.createElement('span');
          c.className = 'detail-chip';
          c.textContent = `${d.label}: ${d.value}`;
          chips.appendChild(c);
        }
        bBlock.appendChild(chips);
      }
      card.appendChild(bBlock);
    }

    if (item.error) {
      const errEl = document.createElement('div');
      errEl.className = 'error-banner';
      errEl.textContent = item.error;
      card.appendChild(errEl);
    }

    const foot = document.createElement('div');
    foot.className = 'card-foot';
    const leftMeta = document.createElement('span');
    const parts = [formatRelativeAgo(item.fetchedAt)];
    if (item.monthlyCost && item.monthlyCost > 0) {
      const sym = item.costCurrency === 'CNY' ? '¥' : '$';
      parts.push(`月费 ${sym}${Number(item.monthlyCost).toFixed(2)}`);
    }
    if (item.notes) parts.push(item.notes);
    leftMeta.textContent = parts.join(' · ');
    foot.appendChild(leftMeta);

    if (item.dashboardUrl) {
      const a = document.createElement('a');
      a.className = 'ext-link';
      a.href = item.dashboardUrl;
      a.target = '_blank';
      a.rel = 'noopener noreferrer';
      a.append('官网面板', icon('open_in_new'));
      foot.appendChild(a);
    }
    card.appendChild(foot);
    return card;
  }

  // Modal Form Rendering
  function populateProviderSelect(selectedId) {
    const sel = $('f-provider');
    sel.innerHTML = '';
    for (const p of state.providers) {
      const opt = document.createElement('option');
      opt.value = p.id;
      opt.textContent = p.name;
      if (p.id === selectedId) opt.selected = true;
      sel.appendChild(opt);
    }
  }

  function stopOAuthPolling() {
    if (state.oauthPollTimer) {
      clearInterval(state.oauthPollTimer);
      state.oauthPollTimer = null;
    }
    state.oauthSession = null;
  }

  function isRemoteBrowserHost() {
    const h = (window.location.hostname || '').toLowerCase();
    return h !== '127.0.0.1' && h !== 'localhost' && h !== '::1' && h !== '[::1]';
  }

  function collectCurrentModalMeta() {
    const provider = $('f-provider').value;
    const desc = state.providers.find((p) => p.id === provider);
    const payload = {
      name: $('f-name').value.trim(),
      provider,
      kind: desc && desc.supportsKindSwitch ? $('f-kind').value : (desc?.defaultKind || 'subscription'),
      badge: $('f-badge').value.trim(),
      pinned: $('f-pinned').checked,
      enabled: $('f-enabled').checked,
      refreshSeconds: Number($('f-refresh').value) || 300,
      sortOrder: Number($('f-sort').value) || 10,
      costCurrency: $('f-cost-currency').value,
      dashboardUrl: $('f-dashboard').value.trim(),
      notes: $('f-notes').value.trim(),
      accountEmail: $('f-account').value.trim(),
    };
    const mc = $('f-monthly-cost').value.trim();
    if (mc !== '') payload.monthlyCost = Number(mc);

    const dynInputs = $('dynamic-fields').querySelectorAll('[data-field-key]');
    for (const inp of dynInputs) {
      const k = inp.dataset.fieldKey;
      const t = inp.dataset.fieldType;
      const raw = inp.value;
      if (t === 'number') {
        if (raw.trim() !== '') payload[k] = Number(raw);
      } else {
        payload[k] = raw;
      }
    }
    return payload;
  }

  async function startOAuthFlow() {
    stopOAuthPolling();
    const meta = collectCurrentModalMeta();
    if (state.editingId) {
      meta.entryId = state.editingId;
    }

    const startBtn = $('btn-oauth-start');
    const startLabel = $('oauth-start-label');
    const origText = startLabel.textContent;
    startBtn.disabled = true;
    startLabel.textContent = '正在创建授权会话…';

    try {
      const sess = await api('/api/oauth/start', {
        method: 'POST',
        body: JSON.stringify(meta),
      });
      state.oauthSession = sess;
      $('oauth-open-link').href = sess.authUrl;
      $('oauth-callback-input').value = '';
      $('oauth-polling-bar').classList.remove('is-error');
      $('oauth-session-box').classList.remove('hidden');

      if (sess.callbackMode === 'device') {
        const codePart = sess.userCode ? `（确认码 ${sess.userCode} 已带入链接）` : '';
        $('oauth-status-text').textContent = `已生成授权链接${codePart}，支持微信扫码登录，网页点击确认后将自动接入…`;
        $('oauth-paste-details').classList.add('hidden');
        $('oauth-paste-details').open = false;
      } else {
        $('oauth-paste-details').classList.remove('hidden');
        $('oauth-status-text').textContent = sess.callbackMode === 'loopback' && !isRemoteBrowserHost()
          ? `正在监听回调 (127.0.0.1:${sess.callbackPort})… 完成浏览器登录后将自动接入`
          : '请点击上方链接完成授权，随后在下方粘贴回调地址（若在运行服务的本机浏览器打开则自动完成）';
        $('oauth-paste-details').open = (sess.callbackMode !== 'loopback' || isRemoteBrowserHost());
      }

      // Attempt immediate new-tab open for desktop convenience; link button remains clickable for mobile WebView.
      try {
        window.open(sess.authUrl, '_blank', 'noopener,noreferrer');
      } catch (_) {}

      state.oauthPollTimer = setInterval(() => {
        pollOAuthStatus(sess.state);
      }, 1500);
    } catch (err) {
      showToast(err.message || '启动授权失败');
    } finally {
      startBtn.disabled = false;
      startLabel.textContent = origText;
    }
  }

  async function pollOAuthStatus(sessionState) {
    if (!state.oauthSession || state.oauthSession.state !== sessionState) return;
    try {
      const st = await api(`/api/oauth/status?state=${encodeURIComponent(sessionState)}`);
      if (st.status === 'completed') {
        stopOAuthPolling();
        closeEntryModal();
        const label = (st.entry && st.entry.name) || st.accountEmail || '新账号';
        showToast(`授权成功，已接入 ${label}`);
        await loadAll();
      } else if (st.status === 'error' || st.status === 'expired') {
        stopOAuthPolling();
        $('oauth-polling-bar').classList.add('is-error');
        $('oauth-status-text').textContent = st.error || '授权已过期或失败，请重新点击上方按钮生成链接';
      } else if (st.status === 'exchanging') {
        $('oauth-status-text').textContent = '已收到回调，正在换取令牌并拉取配额…';
      }
    } catch (_) {
      // Transient network hiccup during poll; keep polling.
    }
  }

  async function refreshOAuthClientBox(providerId) {
    const box = $('oauth-client-box');
    try {
      const st = await api('/api/oauth/clients/' + encodeURIComponent(providerId));
      $('oauth-client-secret').closest('.oauth-paste-row').style.display = '';
      $('oauth-client-secret').placeholder = st.needsSecret
        ? (st.hasSecret ? 'client_secret（已保存，留空保持不变）' : 'client_secret（必填）')
        : 'client_secret（此服务无需）';
      $('oauth-client-secret').disabled = !st.needsSecret;
      $('oauth-client-id').value = '';
      $('oauth-client-id').placeholder = st.clientIdHint ? `client_id（已保存 ${st.clientIdHint}，留空保持不变）` : 'client_id（必填）';
      $('oauth-client-summary').replaceChildren(icon(st.configured ? 'check_circle' : 'warning'), st.configured
        ? ' OAuth 客户端凭据已配置（点击可更新）'
        : ' 请先填写 OAuth 客户端凭据（仅保存在本机）');
      box.open = !st.configured;
    } catch (_) {}
  }

  async function saveOAuthClient() {
    const provider = $('f-provider').value;
    try {
      await api('/api/oauth/clients/' + encodeURIComponent(provider), {
        method: 'PUT',
        body: JSON.stringify({
          clientId: $('oauth-client-id').value.trim(),
          clientSecret: $('oauth-client-secret').value.trim(),
        }),
      });
      $('oauth-client-secret').value = '';
      await refreshOAuthClientBox(provider);
    } catch (e) {
      $('oauth-client-summary').textContent = '保存失败：' + e.message;
    }
  }

  function renderProviderFields(providerId, existingCfg) {
    stopOAuthPolling();
    const desc = state.providers.find((p) => p.id === providerId) || state.providers[0];
    if (!desc) return;
    $('provider-desc').textContent = desc.description || '';

    const isOAuth = !!desc.supportsOAuthFlow;
    const oauthPanel = $('oauth-panel');
    const boundBanner = $('oauth-bound-banner');
    const sessionBox = $('oauth-session-box');
    const saveBtn = $('btn-save');
    const nameInput = $('f-name');
    const nameLabel = $('f-name-label');

    sessionBox.classList.add('hidden');
    if (isOAuth) {
      oauthPanel.classList.remove('hidden');
      refreshOAuthClientBox(providerId);
      const btnLabel = desc.oauthButtonLabel || `打开授权链接（一键接入 ${desc.name}）`;
      if (existingCfg && existingCfg.hasSecret) {
        boundBanner.classList.remove('hidden');
        const emailPart = existingCfg.accountEmail ? ` · ${existingCfg.accountEmail}` : '';
        const refreshPart = existingCfg.hasRefreshToken ? ' · 自动续期' : '';
        boundBanner.replaceChildren(icon('check_circle'), `已绑定授权凭证${emailPart}${refreshPart}`);
        $('oauth-start-label').textContent = '重新登录授权 / 切换账号';
        saveBtn.classList.remove('hidden');
        nameInput.required = true;
        nameLabel.textContent = '显示名称 *';
      } else {
        boundBanner.classList.add('hidden');
        $('oauth-start-label').textContent = btnLabel;
        saveBtn.classList.toggle('hidden', !existingCfg);
        nameInput.required = false;
        nameInput.placeholder = `留空自动命名（如 ${desc.defaultBadge === 'AGY' ? 'Antigravity' : desc.name.split(' ')[0]} + 邮箱前缀）`;
        nameLabel.textContent = '显示名称（可选，留空自动按账号命名）';
      }
    } else {
      oauthPanel.classList.add('hidden');
      boundBanner.classList.add('hidden');
      saveBtn.classList.remove('hidden');
      nameInput.required = true;
      nameInput.placeholder = '如 DeepSeek API / Cursor Pro';
      nameLabel.textContent = '显示名称 *';
    }

    if (!existingCfg) {
      $('f-badge').placeholder = desc.defaultBadge || 'SUB';
      if (!$('f-badge').value) $('f-badge').value = desc.defaultBadge || '';
      $('f-refresh').value = desc.defaultRefreshSeconds || 300;
      $('f-dashboard').placeholder = desc.defaultDashboardUrl || 'https://...';
      $('f-kind').value = desc.defaultKind || 'subscription';
    }

    if (desc.supportsKindSwitch) {
      $('kind-switch-group').classList.remove('hidden');
    } else {
      $('kind-switch-group').classList.add('hidden');
      $('f-kind').value = desc.defaultKind;
    }

    const wrap = $('dynamic-fields');
    wrap.innerHTML = '';
    for (const field of (desc.fields || [])) {
      const grp = document.createElement('div');
      grp.className = 'form-group';
      const lbl = document.createElement('label');
      lbl.textContent = field.label + (field.required ? ' *' : '');
      grp.appendChild(lbl);

      const curVal = existingCfg && existingCfg[field.key] !== undefined && existingCfg[field.key] !== null
        ? existingCfg[field.key]
        : (field.default || '');

      let input;
      if (field.type === 'select') {
        input = document.createElement('select');
        for (const op of (field.options || [])) {
          const o = document.createElement('option');
          o.value = op.value;
          o.textContent = op.label;
          if (String(curVal) === op.value) o.selected = true;
          input.appendChild(o);
        }
      } else {
        input = document.createElement('input');
        input.type = field.type === 'secret' ? 'password' : (field.type === 'number' ? 'number' : 'text');
        if (field.type === 'number') input.step = 'any';
        if (field.type === 'secret' && existingCfg && existingCfg.hasSecret) {
          input.placeholder = `已保存密钥 (${existingCfg.secretHint || '••••'})，留空保持不变`;
        } else {
          input.placeholder = field.placeholder || '';
          input.value = curVal !== '' ? String(curVal) : '';
          if (field.required) input.required = true;
        }
      }
      input.dataset.fieldKey = field.key;
      input.dataset.fieldType = field.type;
      if (isOAuth && !existingCfg && field.key === 'secret') {
        input.addEventListener('input', () => {
          saveBtn.classList.toggle('hidden', !input.value.trim());
        });
      }
      grp.appendChild(input);

      if (field.help) {
        const help = document.createElement('p');
        help.className = 'field-help';
        help.textContent = field.help;
        grp.appendChild(help);
      }
      wrap.appendChild(grp);
    }
  }

  function openEntryModal(id) {
    state.editingId = id;
    const cfg = id ? state.entriesConfig.get(id) : null;
    $('modal-title').textContent = cfg ? `编辑入口 · ${cfg.name}` : '添加订阅 / 余额入口';
    $('f-id').value = cfg ? cfg.id : '';
    $('f-name').value = cfg ? cfg.name : '';
    $('f-badge').value = cfg ? (cfg.badge || '') : '';
    const activePinnedCount = (state.snapshot?.entries || []).filter((e) => !isInitialErrorEntry(e) && e.pinned).length;
    $('f-pinned').checked = cfg ? !!cfg.pinned : (activePinnedCount < 2);
    $('f-enabled').checked = cfg ? !!cfg.enabled : true;
    $('f-refresh').value = cfg ? (cfg.refreshSeconds || 300) : 300;
    $('f-sort').value = cfg ? (cfg.sortOrder || 10) : ((state.snapshot?.entries?.length || 0) + 1) * 10;
    $('f-monthly-cost').value = cfg && cfg.monthlyCost != null ? cfg.monthlyCost : '';
    $('f-cost-currency').value = (cfg && cfg.costCurrency) || 'USD';
    $('f-dashboard').value = (cfg && cfg.dashboardUrl) || '';
    $('f-notes').value = (cfg && cfg.notes) || '';
    $('f-account').value = (cfg && cfg.accountEmail) || '';

    const provId = cfg ? cfg.provider : (state.providers[0]?.id || 'antigravity_oauth');
    populateProviderSelect(provId);
    if (cfg) $('f-kind').value = cfg.kind;
    renderProviderFields(provId, cfg);

    if (cfg) {
      $('delete-zone').classList.remove('hidden');
      $('btn-delete').classList.remove('hidden');
      $('btn-delete-confirm').classList.add('hidden');
    } else {
      $('delete-zone').classList.add('hidden');
    }
    $('entry-modal').classList.remove('hidden');
  }

  function closeEntryModal() {
    stopOAuthPolling();
    $('entry-modal').classList.add('hidden');
    state.editingId = null;
  }

  function openQuickModal(item) {
    const cfg = state.entriesConfig.get(item.id);
    if (!cfg) return;
    $('q-id').value = item.id;
    $('quick-title').textContent = `更新 · ${item.name}`;
    if (item.kind === 'balance') {
      $('q-balance-wrap').classList.remove('hidden');
      $('q-quota-wrap').classList.add('hidden');
      $('q-balance').value = cfg.manualBalance != null ? cfg.manualBalance : 0;
    } else {
      $('q-balance-wrap').classList.add('hidden');
      $('q-quota-wrap').classList.remove('hidden');
      $('q-used').value = cfg.quotaUsed != null ? cfg.quotaUsed : 0;
      $('q-total').value = cfg.quotaTotal != null ? cfg.quotaTotal : 500;
    }
    $('quick-modal').classList.remove('hidden');
  }

  // Event Wiring
  $('btn-theme').addEventListener('click', () => applyTheme(state.theme === 'dark' ? 'light' : 'dark'));
  $('btn-mode').addEventListener('click', () => {
    state.showRemaining = !state.showRemaining;
    localStorage.setItem('subbar_mode', state.showRemaining ? 'remaining' : 'used');
    applyModeButton();
    renderAll();
  });

  $('btn-refresh').addEventListener('click', async () => {
    const btn = $('btn-refresh');
    btn.disabled = true;
    $('refresh-label').textContent = '刷新中…';
    try {
      await api('/api/refresh', { method: 'POST' });
      if (await loadAll()) showToast('全部入口已刷新');
    } catch (err) {
      showToast(err.message);
    } finally {
      btn.disabled = false;
      $('refresh-label').textContent = '刷新';
    }
  });

  $('btn-retry').addEventListener('click', async () => {
    $('btn-retry').disabled = true;
    try {
      await loadAll();
    } finally {
      $('btn-retry').disabled = false;
    }
  });
  $('btn-add').addEventListener('click', () => openEntryModal(null));
  $('modal-close').addEventListener('click', closeEntryModal);
  $('modal-cancel').addEventListener('click', closeEntryModal);

  $('f-provider').addEventListener('change', (e) => {
    $('f-badge').value = '';
    renderProviderFields(e.target.value, null);
  });

  $('btn-oauth-start').addEventListener('click', startOAuthFlow);
  $('btn-oauth-client-save').addEventListener('click', saveOAuthClient);

  $('btn-oauth-copy').addEventListener('click', async () => {
    const href = $('oauth-open-link').href;
    if (!href || href.endsWith('#')) return;
    try {
      await navigator.clipboard.writeText(href);
      showToast('已复制授权链接');
    } catch (_) {
      showToast('请长按上方按钮复制链接');
    }
  });

  $('btn-oauth-submit-url').addEventListener('click', async () => {
    if (!state.oauthSession) {
      showToast('请先点击生成授权链接');
      return;
    }
    const raw = $('oauth-callback-input').value.trim();
    if (!raw) {
      showToast('请先粘贴浏览器回调地址');
      return;
    }
    const btn = $('btn-oauth-submit-url');
    btn.disabled = true;
    btn.textContent = '验证中…';
    try {
      const res = await api('/api/oauth/callback-url', {
        method: 'POST',
        body: JSON.stringify({
          state: state.oauthSession.state,
          callbackUrl: raw,
        }),
      });
      if (res.status === 'completed') {
        stopOAuthPolling();
        closeEntryModal();
        const label = (res.entry && res.entry.name) || res.accountEmail || '新账号';
        showToast(`授权成功，已接入 ${label}`);
        await loadAll();
      }
    } catch (err) {
      showToast(err.message || '验证回调失败');
    } finally {
      btn.disabled = false;
      btn.textContent = '提交回调';
    }
  });

  $('btn-delete').addEventListener('click', () => {
    $('btn-delete').classList.add('hidden');
    $('btn-delete-confirm').classList.remove('hidden');
  });

  $('btn-delete-confirm').addEventListener('click', async () => {
    if (!state.editingId) return;
    try {
      await api(`/api/entries/${encodeURIComponent(state.editingId)}`, { method: 'DELETE' });
      closeEntryModal();
      showToast('已删除入口');
      await loadAll();
    } catch (err) {
      showToast(err.message);
    }
  });

  $('entry-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const id = $('f-id').value;
    const provider = $('f-provider').value;
    const desc = state.providers.find((p) => p.id === provider);
    const resolvedName = $('f-name').value.trim()
      || (desc && desc.name ? desc.name.split(' ')[0] : '订阅账号');
    const payload = {
      name: resolvedName,
      provider,
      kind: desc && desc.supportsKindSwitch ? $('f-kind').value : (desc?.defaultKind || 'subscription'),
      badge: $('f-badge').value.trim(),
      pinned: $('f-pinned').checked,
      enabled: $('f-enabled').checked,
      refreshSeconds: Number($('f-refresh').value) || 300,
      sortOrder: Number($('f-sort').value) || 10,
      costCurrency: $('f-cost-currency').value,
      dashboardUrl: $('f-dashboard').value.trim(),
      notes: $('f-notes').value.trim(),
      accountEmail: $('f-account').value.trim(),
      keepSecret: !!id,
    };
    const mc = $('f-monthly-cost').value.trim();
    if (mc !== '') payload.monthlyCost = Number(mc);

    const dynInputs = $('dynamic-fields').querySelectorAll('[data-field-key]');
    for (const inp of dynInputs) {
      const k = inp.dataset.fieldKey;
      const t = inp.dataset.fieldType;
      const raw = inp.value;
      if (t === 'number') {
        if (raw.trim() !== '') payload[k] = Number(raw);
      } else {
        payload[k] = raw;
      }
    }

    const saveBtn = $('btn-save');
    saveBtn.disabled = true;
    saveBtn.textContent = '保存探测中…';
    try {
      if (id) {
        await api(`/api/entries/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(payload) });
      } else {
        await api('/api/entries', { method: 'POST', body: JSON.stringify(payload) });
      }
      closeEntryModal();
      showToast('已保存并更新快照');
      await loadAll();
    } catch (err) {
      showToast(err.message);
    } finally {
      saveBtn.disabled = false;
      saveBtn.textContent = '保存并刷新';
    }
  });

  $('quick-close').addEventListener('click', () => $('quick-modal').classList.add('hidden'));
  $('quick-cancel').addEventListener('click', () => $('quick-modal').classList.add('hidden'));
  $('quick-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const id = $('q-id').value;
    const cfg = state.entriesConfig.get(id);
    if (!cfg) return;
    const body = {};
    if (cfg.kind === 'balance') {
      body.manualBalance = Number($('q-balance').value) || 0;
    } else {
      body.quotaUsed = Number($('q-used').value) || 0;
      body.quotaTotal = Number($('q-total').value) || 0;
    }
    try {
      await api(`/api/entries/${encodeURIComponent(id)}/quick-update`, {
        method: 'POST',
        body: JSON.stringify(body),
      });
      $('quick-modal').classList.add('hidden');
      showToast('已更新');
      await loadAll();
    } catch (err) {
      showToast(err.message);
    }
  });

  document.querySelectorAll('.filter-tabs .tab').forEach((btn) => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.filter-tabs .tab').forEach((b) => {
        b.classList.toggle('active', b === btn);
        b.setAttribute('aria-pressed', String(b === btn));
      });
      state.filter = btn.dataset.filter;
      if (state.snapshot) renderCards(state.snapshot.entries);
    });
  });

  // Live countdown ticker every 30s + auto snapshot poll every 45s
  setInterval(() => {
    document.querySelectorAll('.countdown-span').forEach((el) => {
      if (el.dataset.reset) el.textContent = formatCountdown(el.dataset.reset);
    });
  }, 30000);
  setInterval(loadAll, 45000);

  applyTheme(state.theme);
  applyModeButton();
  loadAll();
})();
