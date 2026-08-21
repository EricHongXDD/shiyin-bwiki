(() => {
  "use strict";

  const PREVIEW_MODE = new URLSearchParams(window.location.search).get("preview") === "1";
  const EXAMPLE_URL = "https://wiki.biligame.com/klbq/%E7%B1%B3%E9%9B%AA%E5%84%BF%C2%B7%E6%9D%8E/%E8%AF%AD%E9%9F%B3%E5%8F%B0%E8%AF%8D";
  const UNCATEGORIZED_SCENE = "__uncategorized__";
  const TASKS_PER_PAGE = 20;
  const TASK_STATUS_VALUES = ["queued", "running", "paused", "completed", "failed", "cancelled"];

  const state = {
    api: null,
    page: null,
    phase: "idle",
    query: "",
    languageCodes: new Set(),
    sceneKeys: new Set(),
    selected: new Map(),
    allVariants: new Map(),
    tasks: [],
    taskById: new Map(),
    taskOrder: 0,
    downloadDirectory: "",
    includeSubtitles: true,
    view: "download",
    settings: { downloadConcurrency: 4, autoCheckUpdates: true },
    currentVersion: "",
    updateInfo: null,
    updateChecking: false,
    updateError: "",
    currentAudio: null,
    currentAudioKey: "",
    audioLoadingKey: "",
    audioSessionId: 0,
    audioCleanup: null,
    languageMenuOpen: false,
    sceneMenuOpen: false,
    drawerOpen: false,
    activeBatchId: "",
    taskStatusFilter: "all",
    taskPage: 1,
    taskRenderQueued: false,
    drawerReturnFocus: null,
  };

  const elements = {};
  const byId = (id) => document.getElementById(id);
  const icon = (name) => `<svg aria-hidden="true"><use href="#i-${name}"></use></svg>`;

  function escapeHTML(value) {
    return String(value ?? "")
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#39;");
  }

  function displayText(value, fallback = "") {
    const text = String(value ?? "").trim();
    return text || fallback;
  }

  function cleanEntryTitle(value) {
    const text = displayText(value);
    return text.replace(/\s+([^\s]*触发)$/u, "").trim() || text;
  }

  function sceneKey(value) {
    return displayText(value) || UNCATEGORIZED_SCENE;
  }

  function highlight(value, rawQuery) {
    const source = String(value ?? "");
    const query = String(rawQuery ?? "").trim();
    if (!query || query.includes(" ")) return escapeHTML(source);
    const index = source.toLocaleLowerCase().indexOf(query.toLocaleLowerCase());
    if (index < 0) return escapeHTML(source);
    return `${escapeHTML(source.slice(0, index))}<mark>${escapeHTML(source.slice(index, index + query.length))}</mark>${escapeHTML(source.slice(index + query.length))}`;
  }

  function normalizeSearch(value) {
    return String(value ?? "")
      .normalize("NFKC")
      .toLocaleLowerCase()
      .replace(/[\s\p{P}\p{S}]+/gu, "");
  }

  function fuzzyIncludes(haystack, needle) {
    if (!needle) return true;
    if (haystack.includes(needle)) return true;
    if (needle.length < 2) return false;
    let cursor = 0;
    for (let index = 0; index < haystack.length && cursor < needle.length; index += 1) {
      if (haystack[index] === needle[cursor]) cursor += 1;
    }
    return cursor === needle.length;
  }

  function matchesQuery(entry, variants, query) {
    const tokens = String(query ?? "").trim().split(/\s+/).map(normalizeSearch).filter(Boolean);
    if (!tokens.length) return true;
    const fields = [entry.title, entry.category];
    for (const variant of variants) {
      fields.push(variant.transcript, variant.fileName, variant.languageName, variant.language);
    }
    const searchable = fields.map(normalizeSearch).filter(Boolean);
    return tokens.every((token) => searchable.some((field) => fuzzyIncludes(field, token)));
  }

  function debounce(callback, delay) {
    let timer = 0;
    return (...args) => {
      window.clearTimeout(timer);
      timer = window.setTimeout(() => callback(...args), delay);
    };
  }

  // 动态列表重绘后恢复原控件焦点，避免键盘操作被打断。
  function focusWithoutScroll(element) {
    if (!element) return;
    try {
      element.focus({ preventScroll: true });
    } catch {
      element.focus();
    }
  }

  function captureLanguageControlFocus() {
    const active = document.activeElement;
    if (!active || !elements.languageMenu?.contains(active)) return null;
    if (active.dataset.languageCode) return { type: "languageCode", key: active.dataset.languageCode };
    if (active.dataset.languageAction) return { type: "languageAction", key: active.dataset.languageAction };
    if (active.dataset.onlyLanguage) return { type: "onlyLanguage", key: active.dataset.onlyLanguage };
    return null;
  }

  function restoreLanguageControlFocus(snapshot) {
    if (!snapshot) return;
    const selectors = {
      languageCode: "[data-language-code]",
      languageAction: "[data-language-action]",
      onlyLanguage: "[data-only-language]",
    };
    const selector = selectors[snapshot.type];
    if (!selector) return;
    const target = [...elements.languageMenu.querySelectorAll(selector)]
      .find((control) => control.dataset[snapshot.type] === snapshot.key);
    focusWithoutScroll(target);
  }

  function captureSceneControlFocus() {
    const active = document.activeElement;
    if (!active || !elements.sceneMenu?.contains(active)) return null;
    if (active.dataset.sceneKey) return { type: "sceneKey", key: active.dataset.sceneKey };
    if (active.dataset.sceneAction) return { type: "sceneAction", key: active.dataset.sceneAction };
    if (active.dataset.onlyScene) return { type: "onlyScene", key: active.dataset.onlyScene };
    return null;
  }

  function restoreSceneControlFocus(snapshot) {
    if (!snapshot) return;
    const selectors = {
      sceneKey: "[data-scene-key]",
      sceneAction: "[data-scene-action]",
      onlyScene: "[data-only-scene]",
    };
    const selector = selectors[snapshot.type];
    if (!selector) return;
    const target = [...elements.sceneMenu.querySelectorAll(selector)]
      .find((control) => control.dataset[snapshot.type] === snapshot.key);
    focusWithoutScroll(target);
  }

  function captureVoiceCheckboxFocus() {
    const active = document.activeElement;
    if (!active || !elements.voiceList?.contains(active)) return null;
    if (active.dataset.variantSelect) return { type: "variant", key: active.dataset.variantSelect };
    if (active.dataset.entrySelect) return { type: "entry", key: active.dataset.entrySelect };
    return null;
  }

  function restoreVoiceCheckboxFocus(snapshot) {
    if (!snapshot) return;
    const attribute = snapshot.type === "variant" ? "variantSelect" : "entrySelect";
    const selector = snapshot.type === "variant" ? "[data-variant-select]" : "[data-entry-select]";
    const target = [...elements.voiceList.querySelectorAll(selector)]
      .find((input) => input.dataset[attribute] === snapshot.key);
    focusWithoutScroll(target);
  }

  function captureTaskControlFocus() {
    const active = document.activeElement;
    if (!active || !elements.taskPanel?.contains(active)) return null;
    if (active === elements.batchBackButton) return { type: "batchBack", batchId: state.activeBatchId };
    if (active.matches("[data-task-action]")) {
      return { type: "taskAction", id: active.dataset.taskId, action: active.dataset.taskAction };
    }
    if (active.matches("[data-batch-toggle]")) {
      return { type: "batchToggle", batchId: active.dataset.batchToggle };
    }
    if (active.matches("[data-batch-status]")) {
      return { type: "batchStatus", batchId: active.dataset.batchId, status: active.dataset.batchStatus };
    }
    if (active.matches("[data-batch-page]")) {
      return { type: "batchPage", batchId: active.dataset.batchId, page: active.dataset.batchPage };
    }
    return null;
  }

  function restoreTaskControlFocus(snapshot) {
    if (!snapshot) return;
    let exact;
    let fallback;
    if (snapshot.type === "taskAction") {
      const buttons = [...elements.taskPanel.querySelectorAll("[data-task-action]")];
      exact = buttons.find((button) => button.dataset.taskId === snapshot.id && button.dataset.taskAction === snapshot.action);
      fallback = buttons.find((button) => button.dataset.taskId === snapshot.id);
    } else if (snapshot.type === "batchToggle") {
      exact = [...elements.taskList.querySelectorAll("[data-batch-toggle]")]
        .find((button) => button.dataset.batchToggle === snapshot.batchId);
    } else if (snapshot.type === "batchStatus") {
      const buttons = [...elements.taskPanel.querySelectorAll("[data-batch-status]")];
      exact = buttons.find((button) => button.dataset.batchId === snapshot.batchId && button.dataset.batchStatus === snapshot.status);
      fallback = buttons.find((button) => button.dataset.batchId === snapshot.batchId);
    } else if (snapshot.type === "batchPage") {
      const buttons = [...elements.taskPanel.querySelectorAll("[data-batch-page]")];
      exact = buttons.find((button) => button.dataset.batchId === snapshot.batchId && button.dataset.batchPage === snapshot.page);
      fallback = [...elements.taskList.querySelectorAll("[data-batch-toggle]")]
        .find((button) => button.dataset.batchToggle === snapshot.batchId);
    } else if (snapshot.type === "batchBack") {
      exact = elements.batchBackButton;
    }
    focusWithoutScroll(exact || fallback);
  }

  function cacheElements() {
    [
      "appShell", "versionBadge", "taskDrawerTrigger", "headerTaskCount", "urlForm", "urlInput",
      "urlHint", "clearUrlButton", "parseButton", "parseFeedback", "resultCount", "pageSubtitle",
      "warningStack", "resultsToolbar", "searchInput", "languagePicker", "languageTrigger",
      "languageSummary", "languageMenu", "scenePicker", "sceneTrigger", "sceneSummary", "sceneMenu",
      "selectionBar", "selectAllCheckbox", "selectAllLabel",
      "selectedSummary", "clearHiddenSelection", "clearSelection", "includeSubtitlesCheckbox", "batchDownloadButton", "batchDownloadLabel", "batchSubtitleDownloadButton", "batchSubtitleDownloadLabel", "contentState",
      "voiceList", "taskPanel", "taskCount", "clearCompletedButton", "closeTaskDrawer", "directoryPath",
      "chooseDirectoryButton", "taskOverview", "taskBatchView", "taskList", "taskDetailView", "taskContextMenu", "batchBackButton",
      "batchDetailTitle", "batchDetailMeta", "taskStatusFilters", "batchTaskList", "taskPagination",
      "drawerScrim", "bridgeBanner", "toastRegion",
      "downloadView", "settingsView", "downloadTabButton", "settingsTabButton",
      "concurrencyInput", "saveSettingsButton", "settingsSaveStatus", "autoUpdateCheckbox",
      "currentVersionValue", "checkUpdateButton", "downloadUpdateButton", "updateStatus",
    ].forEach((id) => { elements[id] = byId(id); });
  }

  function inferRoleName(title) {
    const text = displayText(title);
    const separatorIndex = text.search(/[\/／]/);
    if (separatorIndex >= 0 && text.slice(0, separatorIndex).trim()) {
      return text.slice(0, separatorIndex).trim();
    }
    for (const suffix of ["语音台词页", "语音台词", "语音"]) {
      if (text.endsWith(suffix) && text.slice(0, -suffix.length).trim()) {
        return text.slice(0, -suffix.length).trim();
      }
    }
    return text;
  }

  function normalizePage(rawPage) {
    const raw = rawPage && typeof rawPage === "object" ? rawPage : {};
    const entries = Array.isArray(raw.entries) ? raw.entries : [];
    const normalizedEntries = entries.map((entryValue, entryIndex) => {
      const entry = entryValue && typeof entryValue === "object" ? entryValue : {};
      const entryId = displayText(entry.id, `entry-${entryIndex + 1}`);
      const variants = (Array.isArray(entry.variants) ? entry.variants : []).map((variantValue, variantIndex) => {
        const variant = variantValue && typeof variantValue === "object" ? variantValue : {};
        const language = displayText(variant.language, "unknown");
        const variantId = displayText(variant.id, `${entryId}-${language}-${variantIndex + 1}`);
        return {
          id: variantId,
          language,
          languageName: displayText(variant.languageName, language === "unknown" ? "未知语言" : language),
          transcript: displayText(variant.transcript),
          fileName: displayText(variant.fileName, `${cleanEntryTitle(entry.title) || "语音"}-${language}.mp3`),
          url: displayText(variant.url),
          _key: `${entryIndex}:${variantIndex}:${variantId}`,
          _entryId: entryId,
          _entryTitle: cleanEntryTitle(entry.title) || `语音 ${entryIndex + 1}`,
          _category: displayText(entry.category),
          _entryIndex: entryIndex,
          _variantIndex: variantIndex,
        };
      });
      return {
        id: entryId,
        category: displayText(entry.category),
        title: cleanEntryTitle(entry.title) || `语音 ${entryIndex + 1}`,
        variants,
        _index: entryIndex,
      };
    }).filter((entry) => entry.variants.length > 0);

    const countedLanguages = new Map();
    entriesFlat(normalizedEntries).forEach((variant) => {
      const current = countedLanguages.get(variant.language) || {
        code: variant.language,
        name: variant.languageName,
        count: 0,
      };
      current.count += 1;
      if (!current.name && variant.languageName) current.name = variant.languageName;
      countedLanguages.set(variant.language, current);
    });

    const rawLanguages = Array.isArray(raw.languages) ? raw.languages : [];
    const languages = [];
    const seenCodes = new Set();
    rawLanguages.forEach((languageValue) => {
      const language = languageValue && typeof languageValue === "object" ? languageValue : {};
      const code = displayText(language.code);
      if (!code || seenCodes.has(code)) return;
      const fallback = countedLanguages.get(code);
      languages.push({
        code,
        name: displayText(language.name, fallback?.name || code),
        count: Number.isFinite(Number(language.count)) ? Number(language.count) : (fallback?.count || 0),
      });
      seenCodes.add(code);
    });
    countedLanguages.forEach((language, code) => {
      if (!seenCodes.has(code)) languages.push(language);
    });

    const countedScenes = new Map();
    normalizedEntries.forEach((entry) => {
      const key = sceneKey(entry.category);
      const current = countedScenes.get(key) || {
        key,
        name: key === UNCATEGORIZED_SCENE ? "未分类" : entry.category,
        count: 0,
        voiceCount: 0,
      };
      current.count += 1;
      current.voiceCount += entry.variants.length;
      countedScenes.set(key, current);
    });

    const title = displayText(raw.title, "BWIKI 语音页面");
    return {
      title,
      roleName: displayText(raw.roleName, inferRoleName(title)),
      sourceUrl: displayText(raw.sourceUrl),
      entries: normalizedEntries,
      languages,
      scenes: [...countedScenes.values()],
      warnings: Array.isArray(raw.warnings) ? raw.warnings : [],
    };
  }

  function entriesFlat(entries) {
    return entries.flatMap((entry) => entry.variants || []);
  }

  function rebuildVariantIndex() {
    state.allVariants.clear();
    if (!state.page) return;
    for (const entry of state.page.entries) {
      for (const variant of entry.variants) state.allVariants.set(variant._key, variant);
    }
  }

  function subtitleFileName(fileName) {
    const text = displayText(fileName, "语音.mp3");
    const extensionIndex = text.lastIndexOf(".");
    return (extensionIndex > 0 ? text.slice(0, extensionIndex) : text) + ".txt";
  }

  function queueItemFromVariant(variant, type = "audio") {
    const isText = type === "text";
    return {
      type,
      id: isText ? variant.id + ":text" : variant.id,
      sourceId: isText ? variant.id + ":text" : variant.id,
      entryId: variant._entryId,
      category: variant._category,
      title: variant._entryTitle,
      language: variant.language,
      languageName: variant.languageName,
      fileName: isText ? subtitleFileName(variant.fileName) : variant.fileName,
      url: isText ? "" : variant.url,
      content: variant.transcript || "",
    };
  }

  function selectedLanguages() {
    if (!state.page) return [];
    return state.page.languages.filter((language) => state.languageCodes.has(language.code));
  }

  function filteredGroups() {
    if (!state.page) return [];
    const groups = [];
    for (const entry of state.page.entries) {
      if (!state.sceneKeys.has(sceneKey(entry.category))) continue;
      const variants = entry.variants.filter((variant) => state.languageCodes.has(variant.language));
      if (!variants.length || !matchesQuery(entry, variants, state.query)) continue;
      groups.push({ entry, variants });
    }
    return groups;
  }

  function visibleVariants(groups = filteredGroups()) {
    return groups.flatMap((group) => group.variants);
  }

  function setPage(page) {
    stopAudio();
    state.page = normalizePage(page);
    state.selected.clear();
    state.query = "";
    state.includeSubtitles = true;
    elements.searchInput.value = "";
    state.languageCodes = new Set(state.page.languages.map((language) => language.code));
    state.sceneKeys = new Set(state.page.scenes.map((scene) => scene.key));
    rebuildVariantIndex();
    renderPage();
  }

  function clearPageState() {
    stopAudio();
    state.page = null;
    state.query = "";
    state.selected.clear();
    state.languageCodes.clear();
    state.sceneKeys.clear();
    elements.searchInput.value = "";
    rebuildVariantIndex();
    renderWarnings();
    renderLanguageMenu();
    renderSceneMenu();
    toggleLanguageMenu(false);
    toggleSceneMenu(false);
  }

  function renderPage() {
    renderWarnings();
    renderLanguageMenu();
    renderSceneMenu();
    renderResults();
  }

  function renderWarnings() {
    if (!state.page?.warnings?.length) {
      elements.warningStack.innerHTML = "";
      return;
    }
    elements.warningStack.innerHTML = state.page.warnings.map((warning) => {
      const message = typeof warning === "string"
        ? warning
        : displayText(warning?.message || warning?.reason || warning?.detail, "页面中有部分内容未能识别。");
      return `<div class="warning-item">${icon("alert")}<span>${escapeHTML(message)}</span></div>`;
    }).join("");
  }

  function renderLanguageMenu() {
    const focusedLanguageControl = captureLanguageControlFocus();
    const languages = state.page?.languages || [];
    const selected = selectedLanguages();
    if (!languages.length) {
      elements.languageSummary.textContent = "无可用语言";
      elements.languageTrigger.disabled = true;
      elements.languageMenu.innerHTML = "";
      return;
    }
    elements.languageTrigger.disabled = false;
    if (selected.length === languages.length) {
      elements.languageSummary.textContent = `全部 ${languages.length} 种语言`;
    } else if (selected.length === 0) {
      elements.languageSummary.textContent = "未选择语言";
    } else if (selected.length === 1) {
      elements.languageSummary.textContent = selected[0].name;
    } else {
      elements.languageSummary.textContent = `已选 ${selected.length} 种语言`;
    }

    const options = languages.map((language) => `
      <div class="language-option">
        <label class="check-control language-check" title="显示${escapeHTML(language.name)}">
          <input type="checkbox" data-language-code="${escapeHTML(language.code)}" ${state.languageCodes.has(language.code) ? "checked" : ""}>
          <span class="custom-check" aria-hidden="true"></span>
          <span class="language-option-name">${escapeHTML(language.name)}</span>
        </label>
        <span class="language-count">${language.count} 条</span>
        <button class="text-button only-language" type="button" data-only-language="${escapeHTML(language.code)}">仅看</button>
      </div>
    `).join("");

    elements.languageMenu.innerHTML = `
      <div class="language-menu-head">
        <strong>显示语言</strong>
        <div class="language-quick-actions">
          <button class="text-button" type="button" data-language-action="all">全选</button>
          <button class="text-button" type="button" data-language-action="none">清空</button>
        </div>
      </div>
      <div class="language-options">${options}</div>
    `;
    restoreLanguageControlFocus(focusedLanguageControl);
  }

  function renderSceneMenu() {
    const focusedSceneControl = captureSceneControlFocus();
    const scenes = state.page?.scenes || [];
    const selected = scenes.filter((scene) => state.sceneKeys.has(scene.key));
    if (!scenes.length) {
      elements.sceneSummary.textContent = "无可用场景";
      elements.sceneTrigger.disabled = true;
      elements.sceneMenu.innerHTML = "";
      return;
    }
    elements.sceneTrigger.disabled = false;
    if (selected.length === scenes.length) {
      elements.sceneSummary.textContent = `全部 ${scenes.length} 个场景`;
    } else if (selected.length === 0) {
      elements.sceneSummary.textContent = "未选择场景";
    } else if (selected.length === 1) {
      elements.sceneSummary.textContent = selected[0].name;
    } else {
      elements.sceneSummary.textContent = `已选 ${selected.length} 个场景`;
    }

    const options = scenes.map((scene) => `
      <div class="scene-option">
        <label class="check-control scene-check" title="显示${escapeHTML(scene.name)}场景">
          <input type="checkbox" data-scene-key="${escapeHTML(scene.key)}" ${state.sceneKeys.has(scene.key) ? "checked" : ""}>
          <span class="custom-check" aria-hidden="true"></span>
          <span class="scene-option-name">${escapeHTML(scene.name)}</span>
        </label>
        <span class="scene-count">${scene.count} 句</span>
        <button class="text-button only-scene" type="button" data-only-scene="${escapeHTML(scene.key)}">仅看</button>
      </div>
    `).join("");

    elements.sceneMenu.innerHTML = `
      <div class="scene-menu-head">
        <strong>显示场景</strong>
        <div class="scene-quick-actions">
          <button class="text-button" type="button" data-scene-action="all">全选</button>
          <button class="text-button" type="button" data-scene-action="none">清空</button>
        </div>
      </div>
      <div class="scene-options">${options}</div>
    `;
    restoreSceneControlFocus(focusedSceneControl);
  }

  function renderResults() {
    const focusedVoiceCheckbox = captureVoiceCheckboxFocus();
    const hasPage = Boolean(state.page);
    elements.searchInput.disabled = !hasPage;
    if (!hasPage) {
      elements.resultCount.textContent = state.phase === "loading" ? "正在解析" : "等待解析";
      elements.pageSubtitle.textContent = state.phase === "error"
        ? "页面解析失败，请根据上方提示检查链接后重试。"
        : "解析页面后，可以搜索、试听并按语言批量选择。";
      elements.voiceList.innerHTML = "";
      elements.selectionBar.hidden = true;
      if (state.phase === "loading") {
        elements.contentState.innerHTML = `<div class="loading-list"><div class="skeleton-card"></div><div class="skeleton-card"></div><div class="skeleton-card"></div></div>`;
      } else if (state.phase === "error") {
        elements.contentState.innerHTML = `
          <div class="empty-state compact">
            <div class="empty-visual" aria-hidden="true"><span>${icon("alert")}</span></div>
            <h3>没有可显示的结果</h3>
            <p>修正页面链接或网络问题后，再次点击“解析页面”。</p>
          </div>`;
      }
      restoreVoiceCheckboxFocus(focusedVoiceCheckbox);
      return;
    }

    const groups = filteredGroups();
    const variants = visibleVariants(groups);
    const allCount = state.page.entries.reduce((sum, entry) => sum + entry.variants.length, 0);
    elements.resultCount.textContent = `${groups.length} 句 · ${variants.length} 条`;
    elements.pageSubtitle.textContent = `${state.page.title} · 共识别 ${state.page.entries.length} 句、${allCount} 条配音 · 下载到文件夹“${state.page.roleName}”`;
    elements.contentState.innerHTML = "";
    elements.selectionBar.hidden = variants.length === 0 && state.selected.size === 0;

    if (!groups.length) {
      const message = state.languageCodes.size === 0
        ? "请在语言筛选中至少选择一种语言。"
        : state.sceneKeys.size === 0
          ? "请在场景筛选中至少选择一个场景。"
          : "没有找到匹配的语音，试试更短的关键词。";
      elements.voiceList.innerHTML = `
        <div class="empty-state compact">
          <div class="empty-visual" aria-hidden="true"><span>${icon("search")}</span></div>
          <h3>当前筛选下没有结果</h3>
          <p>${message}</p>
        </div>`;
      renderSelectionState(groups);
      restoreVoiceCheckboxFocus(focusedVoiceCheckbox);
      return;
    }

    elements.voiceList.innerHTML = groups.map(({ entry, variants: entryVariants }) => {
      const selectedCount = entryVariants.filter((variant) => state.selected.has(variant._key)).length;
      const selectedClass = selectedCount > 0 ? " has-selection" : "";
      const category = entry.category ? `<span class="category-chip">${escapeHTML(entry.category)}</span>` : "";
      const rows = entryVariants.map((variant) => {
        const checked = state.selected.has(variant._key);
        const transcript = variant.transcript || entry.title;
        return `
          <div class="variant-row${checked ? " is-selected" : ""}" data-variant-row="${escapeHTML(variant._key)}">
            <label class="check-control" title="选择这条${escapeHTML(variant.languageName)}语音">
              <input type="checkbox" data-variant-select="${escapeHTML(variant._key)}" ${checked ? "checked" : ""}>
              <span class="custom-check" aria-hidden="true"></span>
            </label>
            <button class="play-button${state.audioLoadingKey === variant._key ? " is-loading" : ""}" type="button" data-action="play" data-key="${escapeHTML(variant._key)}" aria-label="试听${escapeHTML(variant.languageName)}语音" ${variant.url ? "" : "disabled"}>
              ${icon(state.currentAudioKey === variant._key && state.currentAudio && !state.currentAudio.paused ? "pause" : "play")}
            </button>
            <div class="variant-copy">
              <div class="variant-meta">
                <span class="language-tag">${escapeHTML(variant.languageName)}</span>
                <span class="variant-transcript" title="${escapeHTML(transcript)}">${highlight(transcript, state.query)}</span>
              </div>
              <span class="variant-filename" title="${escapeHTML(variant.fileName)}">${escapeHTML(variant.fileName)}</span>
            </div>
            <div class="variant-actions">
              ${variant.transcript ? `<button class="icon-button" type="button" data-action="copy" data-key="${escapeHTML(variant._key)}" aria-label="复制台词" title="复制台词">${icon("copy")}</button>` : ""}
              ${variant.transcript ? `<button class="icon-button download-text" type="button" data-action="download-text" data-key="${escapeHTML(variant._key)}" aria-label="下载字幕" title="下载字幕（${escapeHTML(subtitleFileName(variant.fileName))}）">${icon("file-text")}</button>` : ""}
              <button class="icon-button download-one" type="button" data-action="download-one" data-key="${escapeHTML(variant._key)}" aria-label="下载语音" title="下载语音（可同时下载字幕）" ${variant.url ? "" : "disabled"}>${icon("download")}</button>
            </div>
          </div>`;
      }).join("");

      return `
        <article class="voice-card${selectedClass}" data-entry-card="${escapeHTML(entry.id)}">
          <div class="voice-card-head">
            <label class="check-control entry-select" title="选择本句当前显示的全部语言">
              <input type="checkbox" data-entry-select="${escapeHTML(entry.id)}">
              <span class="custom-check" aria-hidden="true"></span>
            </label>
            <div class="voice-title-wrap">
              <div class="voice-title"><h3 title="${escapeHTML(entry.title)}">${highlight(entry.title, state.query)}</h3>${category}</div>
              <span class="variant-hint">当前显示 ${entryVariants.length} 种语言${selectedCount ? ` · 已选择 ${selectedCount} 条` : ""}</span>
            </div>
            <div class="voice-card-actions">
              <button class="icon-button" type="button" data-action="download-entry" data-entry-id="${escapeHTML(entry.id)}" aria-label="下载本句当前显示的全部语言" title="下载本句当前显示的全部语言">${icon("download")}</button>
            </div>
          </div>
          <div class="variant-list">${rows}</div>
        </article>`;
    }).join("");

    renderSelectionState(groups);
    restoreVoiceCheckboxFocus(focusedVoiceCheckbox);
  }

  function renderSelectionState(groups = filteredGroups()) {
    const visible = visibleVariants(groups);
    const visibleKeys = new Set(visible.map((variant) => variant._key));
    const visibleSelected = visible.filter((variant) => state.selected.has(variant._key)).length;
    const hiddenSelected = [...state.selected.keys()].filter((key) => !visibleKeys.has(key)).length;
    const totalSelected = state.selected.size;

    elements.selectAllCheckbox.checked = visible.length > 0 && visibleSelected === visible.length;
    elements.selectAllCheckbox.indeterminate = visibleSelected > 0 && visibleSelected < visible.length;
    elements.selectAllCheckbox.disabled = visible.length === 0;
    elements.selectAllLabel.textContent = elements.selectAllCheckbox.checked ? "取消全选当前结果" : "全选当前结果";
    const subtitleSelected = [...state.selected.values()].filter((variant) => variant?.transcript).length;
    elements.selectedSummary.textContent = `已选择 ${totalSelected} 条语音 · ${subtitleSelected} 条有字幕`;
    elements.includeSubtitlesCheckbox.checked = state.includeSubtitles;
    elements.clearHiddenSelection.hidden = hiddenSelected === 0;
    elements.clearHiddenSelection.textContent = `清除隐藏的 ${hiddenSelected} 条`;
    elements.clearSelection.hidden = totalSelected === 0;
    elements.batchDownloadButton.disabled = totalSelected === 0;
    elements.batchDownloadLabel.textContent = totalSelected ? `下载所选 (${totalSelected})` : "下载所选";
    elements.batchSubtitleDownloadButton.disabled = subtitleSelected === 0;
    elements.batchSubtitleDownloadLabel.textContent = subtitleSelected ? `仅下载字幕 (${subtitleSelected})` : "仅下载字幕";

    document.querySelectorAll("[data-entry-select]").forEach((checkbox) => {
      const group = groups.find(({ entry }) => entry.id === checkbox.dataset.entrySelect);
      if (!group) return;
      const count = group.variants.filter((variant) => state.selected.has(variant._key)).length;
      checkbox.checked = count === group.variants.length && group.variants.length > 0;
      checkbox.indeterminate = count > 0 && count < group.variants.length;
    });
  }

  function normalizeParseError(rawError) {
    let error = rawError;
    if (typeof error === "string") {
      try { error = JSON.parse(error); } catch { error = { message: error }; }
    }
    if (!error || typeof error !== "object") error = {};
    const rawCode = displayText(error.code, "parse_failed").toLocaleLowerCase();
    const codeAliases = {
      url: "invalid_url",
      domain: "unsupported_host",
      network: "network_error",
      http: "http_error",
      api: "parse_failed",
      noaudio: "no_audio",
      structure: "structure_changed",
    };
    const code = codeAliases[rawCode] || rawCode;
    const titles = {
      invalid_url: "链接格式不正确",
      unsupported_host: "不支持这个网站",
      network_error: "无法连接到 BWIKI",
      timeout: "请求页面超时",
      http_error: "页面请求失败",
      page_not_found: "没有找到这个页面",
      access_denied: "页面拒绝访问",
      rate_limited: "请求过于频繁",
      parse_failed: "页面内容解析失败",
      structure_changed: "页面结构可能已更新",
      no_audio: "页面中没有找到语音",
    };
    const hints = {
      invalid_url: "请粘贴完整的 https://wiki.biligame.com/… 页面链接。",
      unsupported_host: "目前仅支持 wiki.biligame.com 的语音台词页面。",
      network_error: "请检查网络连接、代理或防火墙设置后重试。",
      timeout: "网络响应较慢，请稍后重新解析。",
      page_not_found: "请确认页面未被移动或删除。",
      access_denied: "BWIKI 可能暂时限制了访问，请稍后重试。",
      rate_limited: "请稍候片刻再尝试解析。",
      no_audio: "请确认链接指向角色的“语音台词”页面。",
      structure_changed: "可以保留此链接并向开发者反馈。",
      parse_failed: "请确认页面已公开且包含可播放的语音。",
    };
    const status = Number(error.httpStatus || error.statusCode || error.status || 0);
    const message = displayText(error.message || error.reason, "未能从这个页面提取语音信息。");
    const detail = displayText(error.detail || error.details);
    return {
      code,
      title: displayText(error.title, titles[code] || "页面解析失败"),
      message: detail && !message.includes(detail) ? `${message}：${detail}` : message,
      hint: displayText(error.hint || error.suggestion, hints[code] || "请检查链接后重试；如果问题持续出现，可以保留链接并反馈。"),
      meta: [status ? `HTTP ${status}` : "", error.stage ? `阶段：${error.stage}` : "", error.code ? `错误码：${error.code}` : ""].filter(Boolean).join(" · "),
    };
  }

  function renderParseFeedback(type, payload) {
    if (!type) {
      elements.parseFeedback.innerHTML = "";
      return;
    }
    if (type === "loading") {
      elements.parseFeedback.innerHTML = `<div class="feedback-card loading">${icon("retry")}<div><strong>正在读取页面并识别语音…</strong><p>大型页面可能需要几秒钟，请稍候。</p></div></div>`;
      return;
    }
    if (type === "success") {
      elements.parseFeedback.innerHTML = `<div class="feedback-card success">${icon("check")}<div><strong>${escapeHTML(payload.title)}</strong><p>${escapeHTML(payload.message)}</p></div></div>`;
      return;
    }
    const error = normalizeParseError(payload);
    elements.parseFeedback.innerHTML = `
      <div class="feedback-card error">
        ${icon("alert")}
        <div>
          <strong>${escapeHTML(error.title)}</strong>
          <p>${escapeHTML(error.message)} ${escapeHTML(error.hint)}</p>
          ${error.meta ? `<small>${escapeHTML(error.meta)}</small>` : ""}
        </div>
      </div>`;
  }

  function setParsing(isParsing) {
    elements.parseButton.disabled = isParsing || !state.api;
    elements.parseButton.classList.toggle("is-loading", isParsing);
    elements.parseButton.innerHTML = isParsing
      ? `<span>正在解析</span>${icon("retry")}`
      : `<span>解析页面</span>${icon("arrow")}`;
  }

  async function parsePage(urlValue) {
    const rawUrl = String(urlValue ?? "").trim();
    let parsedUrl;
    try {
      parsedUrl = new URL(rawUrl);
    } catch {
      state.phase = "error";
	  clearPageState();
      renderParseFeedback("error", { code: "invalid_url", message: "输入的内容不是有效的网址。" });
      renderResults();
      elements.urlInput.focus();
      return;
    }
    if (parsedUrl.protocol !== "https:" && parsedUrl.protocol !== "http:") {
      state.phase = "error";
	  clearPageState();
      renderParseFeedback("error", { code: "invalid_url", message: "链接必须使用 HTTP 或 HTTPS 协议。" });
      renderResults();
      return;
    }
    // 与 Go 端保持一致：DNS 完全限定域名末尾允许有一个点。
    const normalizedHostname = parsedUrl.hostname.toLocaleLowerCase().replace(/\.$/, "");
    const standardPort = !parsedUrl.port || (parsedUrl.protocol === "http:" && parsedUrl.port === "80") || (parsedUrl.protocol === "https:" && parsedUrl.port === "443");
    if (normalizedHostname !== "wiki.biligame.com" || parsedUrl.username || parsedUrl.password || !standardPort) {
      state.phase = "error";
	  clearPageState();
      renderParseFeedback("error", { code: "unsupported_host", message: `无法解析 ${parsedUrl.hostname} 的页面。` });
      renderResults();
      return;
    }
    if (!state.api) {
      showToast("请在桌面应用中运行", "当前页面没有连接到解析服务。", "warning");
      return;
    }

    stopAudio();
    state.phase = "loading";
	clearPageState();
    setParsing(true);
    renderParseFeedback("loading");
    renderResults();
    try {
      const response = await state.api.ParsePage(rawUrl);
      if (!response || response.ok === false || !response.page) {
        throw { __parseError: true, detail: response?.error || { code: "parse_failed" } };
      }
      state.phase = "success";
      setPage(response.page);
      const variants = entriesFlat(state.page.entries);
      renderParseFeedback("success", {
        title: "解析完成",
        message: `找到 ${state.page.entries.length} 句台词、${variants.length} 条配音，包含 ${state.page.languages.length} 种语言。`,
      });
    } catch (error) {
      state.phase = "error";
	  clearPageState();
      const detail = error?.__parseError ? error.detail : {
        code: "network_error",
        message: displayText(error?.message || error, "请求解析服务时发生未知错误。"),
      };
      renderParseFeedback("error", detail);
      renderResults();
    } finally {
      setParsing(false);
    }
  }

  function updateInputClearButton() {
    elements.clearUrlButton.hidden = !elements.urlInput.value;
  }

  function toggleLanguageMenu(force) {
    const next = typeof force === "boolean" ? force : !state.languageMenuOpen;
    if (next && elements.languageTrigger.disabled) return;
    if (next) toggleSceneMenu(false);
    state.languageMenuOpen = next;
    elements.languageMenu.hidden = !next;
    elements.languageTrigger.setAttribute("aria-expanded", String(next));
  }

  function toggleSceneMenu(force) {
    const next = typeof force === "boolean" ? force : !state.sceneMenuOpen;
    if (next && elements.sceneTrigger.disabled) return;
    if (next) {
      state.languageMenuOpen = false;
      elements.languageMenu.hidden = true;
      elements.languageTrigger.setAttribute("aria-expanded", "false");
    }
    state.sceneMenuOpen = next;
    elements.sceneMenu.hidden = !next;
    elements.sceneTrigger.setAttribute("aria-expanded", String(next));
  }

  function toggleDrawer(force) {
    const next = typeof force === "boolean" ? force : !state.drawerOpen;
	const wasOpen = state.drawerOpen;
	if (next && !wasOpen) state.drawerReturnFocus = document.activeElement;
    state.drawerOpen = next;
    elements.taskPanel.classList.toggle("is-open", next);
    elements.taskDrawerTrigger.setAttribute("aria-expanded", String(next));
    elements.drawerScrim.hidden = !next;
    document.body.style.overflow = next && window.matchMedia("(max-width: 1120px)").matches ? "hidden" : "";
	syncDrawerAccessibility();
	if (next && window.matchMedia("(max-width: 1120px)").matches) {
	  window.requestAnimationFrame(() => elements.closeTaskDrawer.focus());
	} else if (wasOpen && state.drawerReturnFocus?.focus) {
	  state.drawerReturnFocus.focus();
	  state.drawerReturnFocus = null;
	}
  }

	function drawerFocusableElements() {
	  return [...elements.taskPanel.querySelectorAll('button:not(:disabled), input:not(:disabled), [href], [tabindex]:not([tabindex="-1"])')]
		.filter((element) => !element.hidden && element.getClientRects().length > 0);
	}

	function syncDrawerAccessibility() {
	  const narrow = window.matchMedia("(max-width: 1120px)").matches;
	  const modalOpen = narrow && state.drawerOpen;
	  const background = document.querySelectorAll(".app-header, .source-panel, .results-panel");
	  if (narrow) {
		elements.taskPanel.inert = !modalOpen;
		elements.taskPanel.setAttribute("aria-hidden", String(!modalOpen));
		elements.taskPanel.setAttribute("role", "dialog");
		elements.taskPanel.setAttribute("aria-modal", String(modalOpen));
	  } else {
		elements.taskPanel.inert = false;
		elements.taskPanel.removeAttribute("aria-hidden");
		elements.taskPanel.removeAttribute("role");
		elements.taskPanel.removeAttribute("aria-modal");
	  }
	  background.forEach((element) => { element.inert = modalOpen; });
	}

  function stopAudio() {
    const audio = state.currentAudio;
    const cleanup = state.audioCleanup;
    // 先使当前会话失效并解绑事件，避免旧音频的延迟事件误伤下一条音频。
    state.audioSessionId += 1;
    state.currentAudio = null;
    state.currentAudioKey = "";
    state.audioLoadingKey = "";
    state.audioCleanup = null;
    cleanup?.();
    if (audio) {
      try { audio.pause(); } catch {}
      try {
        audio.removeAttribute("src");
        audio.load();
      } catch {}
    }
    updateAudioButtons();
  }

  function isCurrentAudioSession(audio, sessionId) {
    return state.currentAudio === audio && state.audioSessionId === sessionId;
  }

  function isExpectedAudioInterruption(error) {
    const message = String(error?.message ?? "");
    return error?.name === "AbortError"
      || /play\(\) request was interrupted|interrupted by a call to pause|interrupted by a new load request/i.test(message);
  }

  function failAudioSession(audio, sessionId, error) {
    if (!isCurrentAudioSession(audio, sessionId)) return;
    stopAudio();
    showToast("试听失败", displayText(error?.message, "无法加载远程音频，请检查网络连接。"), "error");
  }

  function updateAudioButtons() {
    document.querySelectorAll('[data-action="play"]').forEach((button) => {
      const playing = button.dataset.key === state.currentAudioKey && state.currentAudio && !state.currentAudio.paused;
      button.classList.toggle("is-loading", button.dataset.key === state.audioLoadingKey);
      button.innerHTML = icon(playing ? "pause" : "play");
      button.setAttribute("aria-label", playing ? "暂停试听" : "试听语音");
    });
  }

  async function toggleAudio(key) {
    const variant = state.allVariants.get(key);
    if (!variant?.url) {
      showToast("无法试听", "这条语音没有可用的远程地址。", "warning");
      return;
    }
    if (state.currentAudioKey === key && state.currentAudio) {
      const audio = state.currentAudio;
      const sessionId = state.audioSessionId;
      if (audio.paused) {
        state.audioLoadingKey = key;
        updateAudioButtons();
        try {
          await audio.play();
        } catch (error) {
          if (!isCurrentAudioSession(audio, sessionId)) return;
          if (isExpectedAudioInterruption(error)) {
            state.audioLoadingKey = "";
            updateAudioButtons();
            return;
          }
          failAudioSession(audio, sessionId, error);
        }
      } else {
        state.audioLoadingKey = "";
        audio.pause();
      }
      updateAudioButtons();
      return;
    }

    stopAudio();
    const sessionId = state.audioSessionId;
    const audio = new Audio();
    audio.preload = "metadata";
    state.currentAudio = audio;
    state.currentAudioKey = key;
    state.audioLoadingKey = key;
    const onPlaying = () => {
      if (!isCurrentAudioSession(audio, sessionId)) return;
      state.audioLoadingKey = "";
      updateAudioButtons();
    };
    const onPause = () => {
      if (!isCurrentAudioSession(audio, sessionId)) return;
      state.audioLoadingKey = "";
      updateAudioButtons();
    };
    const onEnded = () => {
      if (!isCurrentAudioSession(audio, sessionId)) return;
      state.audioLoadingKey = "";
      updateAudioButtons();
    };
    const onError = () => failAudioSession(audio, sessionId, new Error("远程音频加载失败"));
    audio.addEventListener("playing", onPlaying);
    audio.addEventListener("pause", onPause);
    audio.addEventListener("ended", onEnded);
    audio.addEventListener("error", onError);
    state.audioCleanup = () => {
      audio.removeEventListener("playing", onPlaying);
      audio.removeEventListener("pause", onPause);
      audio.removeEventListener("ended", onEnded);
      audio.removeEventListener("error", onError);
    };
    audio.src = variant.url;
    updateAudioButtons();
    try {
      await audio.play();
    } catch (error) {
      if (!isCurrentAudioSession(audio, sessionId)) return;
      if (isExpectedAudioInterruption(error)) {
        state.audioLoadingKey = "";
        updateAudioButtons();
        return;
      }
      failAudioSession(audio, sessionId, error);
    }
  }

  async function copyTranscript(key) {
    const text = state.allVariants.get(key)?.transcript;
    if (!text) return;
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text);
      } else {
        const textarea = document.createElement("textarea");
        textarea.value = text;
        textarea.style.position = "fixed";
        textarea.style.opacity = "0";
        document.body.appendChild(textarea);
        textarea.select();
        document.execCommand("copy");
        textarea.remove();
      }
      showToast("台词已复制", text.length > 38 ? `${text.slice(0, 38)}…` : text, "success", 2200);
    } catch {
      showToast("复制失败", "系统剪贴板暂时不可用。", "error");
    }
  }

  async function chooseDirectory() {
    if (!state.api) {
      showToast("无法选择目录", "请在桌面应用中运行。", "warning");
      return "";
    }
    elements.chooseDirectoryButton.disabled = true;
    try {
      const result = await state.api.ChooseDownloadDirectory();
      const path = typeof result === "string" ? result : displayText(result?.directory || result?.path);
      if (path) {
        state.downloadDirectory = path;
        renderDirectory();
      }
      return path;
    } catch (error) {
      showToast("选择目录失败", displayText(error?.message || error, "无法打开目录选择窗口。"), "error");
      return "";
    } finally {
      elements.chooseDirectoryButton.disabled = false;
    }
  }

  async function ensureDirectory() {
    if (state.downloadDirectory) return true;
    const directory = await chooseDirectory();
    if (!directory) showToast("尚未开始下载", "请先选择语音的保存目录。", "warning");
    return Boolean(directory);
  }

  function unwrapTasks(result) {
    if (Array.isArray(result)) return result;
    if (Array.isArray(result?.tasks)) return result.tasks;
    if (result && typeof result === "object" && (result.id || result.taskId)) return [result];
    return [];
  }

  async function queueDownloads(variants, options = {}) {
    const candidates = Array.isArray(variants) ? variants : [];
    const textOnly = options.textOnly === true;
    const audioVariants = textOnly ? [] : candidates.filter((variant) => variant?.url);
    const subtitleCandidates = textOnly ? candidates : audioVariants;
    const subtitleVariants = subtitleCandidates.filter((variant) => variant?.transcript);
    if (textOnly && !subtitleVariants.length) {
      showToast("没有可下载的字幕", "所选语音没有可用的台词文本。", "warning");
      return;
    }
    if (!textOnly && !audioVariants.length) {
      showToast("没有可下载的语音", "所选内容缺少有效的音频地址。", "warning");
      return;
    }
    if (!state.api) return;
    if (!options.directory && !(await ensureDirectory())) return;
    const requestDirectory = options.directory || state.downloadDirectory;
    const button = options.button;
    if (button) button.disabled = true;
    try {
      const audioItems = textOnly ? [] : audioVariants.map((variant) => queueItemFromVariant(variant));
      const includeSubtitles = !textOnly && (options.includeSubtitles ?? state.includeSubtitles);
      const subtitleItems = includeSubtitles || textOnly
        ? subtitleVariants.map((variant) => queueItemFromVariant(variant, "text"))
        : [];
      const items = audioItems.concat(subtitleItems);
      const response = await state.api.QueueDownloads({
        directory: requestDirectory,
        roleName: options.roleName ?? state.page?.roleName ?? "",
        items,
      });
      const tasks = unwrapTasks(response);
      tasks.forEach((task) => upsertTask(task, false));
      renderTasks();
      showToast(
        textOnly ? "已创建字幕下载批次" : "已创建下载批次",
        "共 " + items.length + " 个文件正在等待下载。",
        "success",
      );
      if (options.clearSelection) {
        state.selected.clear();
        renderResults();
      }
      if (window.matchMedia("(max-width: 1120px)").matches) toggleDrawer(true);
    } catch (error) {
      showToast("创建下载任务失败", displayText(error?.message || error, "请稍后重试。"), "error");
    } finally {
      if (button === elements.batchDownloadButton || button === elements.batchSubtitleDownloadButton) {
        renderSelectionState();
      } else if (button) {
        button.disabled = false;
      }
    }
  }

  function normalizeStatus(value) {
    const status = displayText(value, "queued").toLocaleLowerCase();
    const aliases = {
      pending: "queued",
      waiting: "queued",
      downloading: "running",
      active: "running",
      pause: "paused",
      done: "completed",
      complete: "completed",
      success: "completed",
      error: "failed",
      canceled: "cancelled",
      cancel: "cancelled",
      removed: "cancelled",
    };
    return aliases[status] || status;
  }

  function numberValue(...values) {
    for (const value of values) {
      const number = Number(value);
      if (Number.isFinite(number)) return number;
    }
    return 0;
  }

  function normalizeTask(rawTask, previous) {
    const raw = rawTask && typeof rawTask === "object" ? rawTask : {};
    const id = displayText(raw.id ?? raw.taskId, previous?.id || `task-${Date.now()}-${Math.random().toString(16).slice(2)}`);
    const status = normalizeStatus(raw.status || raw.state || previous?.status);
    const downloadedBytes = Math.max(0, numberValue(raw.downloadedBytes, raw.completedBytes, raw.bytesDownloaded, raw.receivedBytes, raw.bytes, previous?.downloadedBytes));
    const totalBytes = Math.max(0, numberValue(raw.totalBytes, raw.size, raw.contentLength, raw.total, previous?.totalBytes));
    let progress = numberValue(raw.progress, raw.percent, previous?.progress);
    if (progress > 0 && progress <= 1) progress *= 100;
    if (!progress && totalBytes) progress = downloadedBytes / totalBytes * 100;
    if (status === "completed") progress = 100;
    progress = Math.max(0, Math.min(100, progress));
    return {
      ...previous,
      ...raw,
      id,
      batchId: displayText(raw.batchId ?? raw.batchID ?? raw.groupId, previous?.batchId),
      sourceId: displayText(raw.sourceId ?? raw.sourceID, previous?.sourceId),
      status,
      type: displayText(raw.type, previous?.type || "audio"),
      title: displayText(raw.title || raw.entryTitle || raw.displayName, previous?.title || "语音下载"),
      category: displayText(raw.category, previous?.category),
      fileName: displayText(raw.fileName || raw.filename || raw.name, previous?.fileName || "正在准备文件…"),
      content: displayText(raw.content ?? raw.transcript, previous?.content),
      languageName: displayText(raw.languageName || raw.language, previous?.languageName),
      directory: displayText(raw.directory, previous?.directory),
      createdAt: raw.createdAt || previous?.createdAt || "",
      downloadedBytes,
      totalBytes,
      progress,
      speed: Math.max(0, numberValue(raw.speedBytesPerSecond, raw.bytesPerSecond, raw.speed, previous?.speed)),
      error: status === "failed"
        ? displayText(raw.error?.message || raw.error || raw.errorMessage || raw.message, previous?.error)
        : "",
      _order: previous?._order ?? ++state.taskOrder,
      _updatedAtKey: sortableTimestamp(raw.updatedAt) || previous?._updatedAtKey || "",
    };
  }

  function sortableTimestamp(value) {
    const text = String(value ?? "").trim();
    const match = text.match(/^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?Z$/);
    if (!match) return "";
    return `${match[1]}.${(match[2] || "").padEnd(9, "0")}`;
  }

  function upsertTask(rawTask, shouldRender = true) {
    if (Array.isArray(rawTask) && rawTask.length === 1) rawTask = rawTask[0];
    if (!rawTask || typeof rawTask !== "object") return;
    const rawId = displayText(rawTask.id ?? rawTask.taskId);
    const existing = rawId ? state.taskById.get(rawId) : null;
    if (existing) {
      const incomingKey = sortableTimestamp(rawTask.updatedAt);
      const currentKey = existing._updatedAtKey || "";
      if (incomingKey && currentKey && incomingKey < currentKey) return;
      Object.assign(existing, normalizeTask(rawTask, existing));
    } else {
      const task = normalizeTask(rawTask);
      state.tasks.push(task);
      state.taskById.set(task.id, task);
    }
    if (shouldRender) scheduleTaskRender();
  }

	function scheduleTaskRender() {
	  if (state.taskRenderQueued) return;
	  state.taskRenderQueued = true;
	  window.requestAnimationFrame(() => {
		state.taskRenderQueued = false;
		renderTasks();
	  });
	}

  function formatBytes(value) {
    const bytes = Math.max(0, Number(value) || 0);
    if (!bytes) return "0 B";
    const units = ["B", "KB", "MB", "GB", "TB"];
    const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
    const number = bytes / 1024 ** index;
    return `${number >= 100 || index === 0 ? number.toFixed(0) : number.toFixed(1)} ${units[index]}`;
  }

  function statusDetails(status) {
    const details = {
      queued: ["排队中", "queued"],
      running: ["下载中", "running"],
      paused: ["已暂停", "paused"],
      completed: ["已完成", "completed"],
      failed: ["下载失败", "failed"],
      cancelled: ["已取消", "cancelled"],
    };
    return details[status] || ["等待中", "queued"];
  }

  function taskBatchKey(task) {
    return displayText(task.batchId) || `legacy:${task.id}`;
  }

  function buildTaskBatches() {
    const batches = new Map();
    const orderedTasks = [...state.tasks].sort((a, b) => a._order - b._order);
    orderedTasks.forEach((task) => {
      const id = taskBatchKey(task);
      let batch = batches.get(id);
      if (!batch) {
        batch = { id, tasks: [], _order: task._order, createdAt: task.createdAt };
        batches.set(id, batch);
      }
      batch.tasks.push(task);
      batch._order = Math.max(batch._order, task._order);
      if (!batch.createdAt && task.createdAt) batch.createdAt = task.createdAt;
    });
    return [...batches.values()].sort((a, b) => b._order - a._order);
  }

  function summarizeBatch(batch) {
    const counts = Object.fromEntries(TASK_STATUS_VALUES.map((status) => [status, 0]));
    let downloadedBytes = 0;
    let totalBytes = 0;
    let speed = 0;
    let hasUnknownActiveSize = false;
    batch.tasks.forEach((task) => {
      counts[task.status] = (counts[task.status] || 0) + 1;
      downloadedBytes += task.downloadedBytes;
      totalBytes += task.totalBytes;
      if (task.status === "running") speed += task.speed;
      if ((task.status === "running" || task.status === "queued") && !task.totalBytes) hasUnknownActiveSize = true;
    });
    const total = batch.tasks.length;
    const completed = counts.completed || 0;
    const active = (counts.running || 0) + (counts.queued || 0);
    let status = "queued";
    if (completed === total && total > 0) status = "completed";
    else if (counts.running) status = "running";
    else if (counts.queued) status = "queued";
    else if (counts.paused) status = "paused";
    else if (counts.failed) status = "failed";
    else if (counts.cancelled) status = "cancelled";

    let progress = 0;
    if (completed === total && total > 0) progress = 100;
    else if (hasUnknownActiveSize) progress = total ? completed / total * 100 : 0;
    else if (totalBytes > 0) progress = downloadedBytes / totalBytes * 100;
    progress = Math.max(0, Math.min(100, progress));

    let [statusLabel, statusClass] = statusDetails(status);
    if (!active && counts.failed > 0 && counts.failed < total) statusLabel = "部分失败";
    else if (!active && counts.cancelled > 0 && completed > 0) statusLabel = "部分完成";
    return {
      counts,
      total,
      completed,
      active,
      status,
      statusLabel,
      statusClass,
      progress,
      progressText: hasUnknownActiveSize ? "正在获取大小" : `${Math.round(progress)}%`,
      downloadedBytes,
      totalBytes,
      speed,
      hasUnknownActiveSize,
    };
  }

  function formatBatchTime(value) {
    const date = new Date(value);
    if (!value || Number.isNaN(date.getTime())) return "历史任务";
    const now = new Date();
    const time = new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false }).format(date);
    if (date.toDateString() === now.toDateString()) return `今天 ${time}`;
    return new Intl.DateTimeFormat("zh-CN", {
      month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false,
    }).format(date);
  }

  function batchLanguageSummary(batch) {
    const languages = [...new Set(batch.tasks.map((task) => task.languageName).filter(Boolean))];
    if (!languages.length) return "语音下载";
    if (languages.length === 1) return languages[0];
    return `${languages.length} 种语言`;
  }

  function taskSubtitleVariant(task) {
    const sourceId = displayText(task?.sourceId);
    return sourceId ? state.allVariants.get(sourceId) : null;
  }

  function taskSubtitleContent(task) {
    return displayText(task?.content) || displayText(taskSubtitleVariant(task)?.transcript);
  }

  function taskHasSubtitle(task) {
    if (!task || task.type !== "audio") return false;
    const expectedFileName = subtitleFileName(task.fileName);
    return state.tasks.some((item) => item.id !== task.id
      && item.type === "text"
      && item.fileName === expectedFileName
      && item.directory === task.directory);
  }

  function canDownloadTaskSubtitle(task) {
    return Boolean(task
      && task.status === "completed"
      && task.type === "audio"
      && taskSubtitleContent(task)
      && !taskHasSubtitle(task));
  }

  function taskActions(task) {
    const button = (action, iconName, title, danger = false) => `
      <button class="icon-button task-action${danger ? " button-danger-ghost" : ""}" type="button" data-task-action="${action}" data-task-id="${escapeHTML(task.id)}" aria-label="${title}" title="${title}">${icon(iconName)}</button>`;
    if (task.status === "running") return button("pause", "pause", "暂停") + button("cancel", "stop", "取消", true);
    if (task.status === "queued") return button("cancel", "stop", "取消", true);
    if (task.status === "paused") return button("resume", "play", "继续") + button("cancel", "stop", "取消", true);
    if (task.status === "failed") return button("retry", "retry", "重试") + button("remove", "trash", "移除", true);
    if (task.status === "completed") {
      const subtitleButton = canDownloadTaskSubtitle(task)
        ? button("download-subtitle", "file-text", "下载字幕")
        : "";
      return button("open", "folder", "打开所在文件夹") + subtitleButton + button("remove", "trash", "移除");
    }
    if (task.status === "cancelled") return button("retry", "retry", "重新下载") + button("remove", "trash", "移除");
    return button("remove", "trash", "移除");
  }

  function renderTaskCard(task) {
      const [statusLabel, statusClass] = statusDetails(task.status);
      const activeClass = task.status === "running" || task.status === "queued" ? " is-active" : "";
      const failedClass = task.status === "failed" ? " is-failed" : "";
      const completedClass = task.status === "completed" ? " is-completed" : "";
      const byteText = task.totalBytes
        ? `${formatBytes(task.downloadedBytes)} / ${formatBytes(task.totalBytes)}`
        : (task.status === "queued" ? "等待获取文件大小" : formatBytes(task.downloadedBytes));
      const speedText = task.status === "running" && task.speed > 0 ? `${formatBytes(task.speed)}/s` : `${Math.round(task.progress)}%`;
      const subtitle = [task.type === "text" ? "字幕" : task.languageName, task.category, task.title].filter(Boolean).join(" · ");
      return `
        <article class="task-card task-item-card${activeClass}${failedClass}${completedClass}" data-task-card="${escapeHTML(task.id)}">
          <div class="task-card-head">
            <span class="task-file-icon">${icon(task.type === "text" ? "file-text" : "wave")}</span>
            <div class="task-copy">
              <strong title="${escapeHTML(task.fileName)}">${escapeHTML(task.fileName)}</strong>
              <span title="${escapeHTML(subtitle)}">${escapeHTML(subtitle || "BWIKI 语音")}</span>
            </div>
            <span class="status-pill status-${statusClass}">${statusLabel}</span>
          </div>
          <div class="progress-track" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.round(task.progress)}">
            <div class="progress-value" style="width:${task.progress.toFixed(2)}%"></div>
          </div>
          <div class="task-progress-meta"><span>${byteText}</span><strong>${speedText}</strong></div>
          ${task.error ? `<div class="task-error">${icon("alert")}<span>${escapeHTML(task.error)}</span></div>` : ""}
          <div class="task-actions">${taskActions(task)}</div>
        </article>`;
  }

  function renderBatchCard(batch) {
    const summary = summarizeBatch(batch);
    const activeClass = summary.status === "running" || summary.status === "queued" ? " is-active" : "";
    const failedClass = summary.status === "failed" ? " is-failed" : "";
    const completedClass = summary.status === "completed" ? " is-completed" : "";
    const subtitleTasks = batch.tasks.filter(canDownloadTaskSubtitle);
    const firstTitle = displayText(batch.tasks[0]?.title, "语音下载");
    const moreText = batch.tasks.length > 1 ? ` 等 ${batch.tasks.length} 条` : " · 1 条语音";
    const progressMeta = summary.totalBytes > 0
      ? `${formatBytes(summary.downloadedBytes)} / ${formatBytes(summary.totalBytes)}`
      : `${summary.completed} / ${summary.total} 条完成`;
    const countSummary = TASK_STATUS_VALUES
      .filter((status) => summary.counts[status] > 0)
      .map((status) => `<span class="batch-count batch-count-${status}">${statusDetails(status)[0]} ${summary.counts[status]}</span>`)
      .join("");
    const subtitleAction = subtitleTasks.length
      ? `<div class="batch-actions">
          <button class="button button-ghost button-compact" type="button" data-batch-action="download-subtitles" data-batch-id="${escapeHTML(batch.id)}" title="下载这个批次中尚未保存的字幕">
            ${icon("file-text")}<span>下载字幕 (${subtitleTasks.length})</span>
          </button>
          <span class="batch-action-note">仅处理未下载字幕</span>
        </div>`
      : "";
    return `
      <article class="task-batch-card${activeClass}${failedClass}${completedClass}" data-batch-card="${escapeHTML(batch.id)}">
        <button class="task-batch-toggle" type="button" data-batch-toggle="${escapeHTML(batch.id)}" aria-label="查看下载批次，共 ${summary.total} 条语音">
          <span class="task-file-icon batch-file-icon">${icon("tasks")}</span>
          <span class="task-copy batch-copy">
            <strong title="${escapeHTML(firstTitle)}">${escapeHTML(firstTitle)}${escapeHTML(moreText)}</strong>
            <span>${escapeHTML(formatBatchTime(batch.createdAt))} · ${escapeHTML(batchLanguageSummary(batch))}</span>
          </span>
          <span class="status-pill status-${summary.statusClass}">${summary.statusLabel}</span>
          <svg class="batch-chevron" aria-hidden="true"><use href="#i-chevron"></use></svg>
        </button>
        <div class="progress-track" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.round(summary.progress)}">
          <div class="progress-value" style="width:${summary.progress.toFixed(2)}%"></div>
        </div>
        <div class="task-progress-meta">
          <span>${progressMeta}</span>
          <strong>${summary.speed > 0 ? `${formatBytes(summary.speed)}/s` : summary.progressText}</strong>
        </div>
        <div class="batch-counts">${countSummary}</div>
        ${subtitleAction}
      </article>`;
  }

  function renderBatchList(batches) {
    elements.taskBatchView.hidden = false;
    elements.taskDetailView.hidden = true;
    if (!batches.length) {
      elements.taskList.innerHTML = `
        <div class="tasks-empty">
          <span>${icon("download")}</span>
          <h3>暂无下载批次</h3>
          <p>每次下载会汇总为一个批次，点击批次可查看语音明细。</p>
        </div>`;
      return;
    }
    elements.taskList.innerHTML = batches.map(renderBatchCard).join("");
  }

  function renderBatchDetail(batch) {
    const summary = summarizeBatch(batch);
    const status = TASK_STATUS_VALUES.includes(state.taskStatusFilter) ? state.taskStatusFilter : "all";
    state.taskStatusFilter = status;
    const filteredTasks = status === "all"
      ? batch.tasks
      : batch.tasks.filter((task) => task.status === status);
    const pageCount = Math.max(1, Math.ceil(filteredTasks.length / TASKS_PER_PAGE));
    state.taskPage = Math.max(1, Math.min(pageCount, Number(state.taskPage) || 1));
    const start = (state.taskPage - 1) * TASKS_PER_PAGE;
    const pageTasks = filteredTasks.slice(start, start + TASKS_PER_PAGE);

    elements.taskBatchView.hidden = true;
    elements.taskDetailView.hidden = false;
    elements.batchDetailTitle.textContent = `下载批次 · ${summary.total} 条语音`;
    elements.batchDetailMeta.textContent = `${formatBatchTime(batch.createdAt)} · 已完成 ${summary.completed} 条`;
    elements.taskStatusFilters.innerHTML = ["all", ...TASK_STATUS_VALUES].map((value) => {
      const label = value === "all" ? "全部" : statusDetails(value)[0];
      const count = value === "all" ? summary.total : summary.counts[value];
      return `<button class="task-status-filter${status === value ? " is-active" : ""}" type="button" data-batch-status="${value}" data-batch-id="${escapeHTML(batch.id)}" aria-pressed="${status === value}">${label}<span>${count}</span></button>`;
    }).join("");
    elements.batchTaskList.innerHTML = pageTasks.length
      ? pageTasks.map(renderTaskCard).join("")
      : `<div class="task-filter-empty"><strong>此状态下没有语音</strong><span>选择其他状态查看下载明细。</span></div>`;
    elements.taskPagination.hidden = filteredTasks.length <= TASKS_PER_PAGE;
    elements.taskPagination.innerHTML = `
      <button class="button button-ghost button-compact" type="button" data-batch-page="prev" data-batch-id="${escapeHTML(batch.id)}" ${state.taskPage <= 1 ? "disabled" : ""}>上一页</button>
      <span>第 ${state.taskPage} / ${pageCount} 页 · ${filteredTasks.length} 条</span>
      <button class="button button-ghost button-compact" type="button" data-batch-page="next" data-batch-id="${escapeHTML(batch.id)}" ${state.taskPage >= pageCount ? "disabled" : ""}>下一页</button>`;
  }

  function closeTaskContextMenu() {
    elements.taskContextMenu.hidden = true;
    elements.taskContextMenu.innerHTML = "";
  }

  function openTaskContextMenu(task, clientX, clientY) {
    if (!task || task.status !== "completed" || task.type !== "audio") return;
    const canDownload = canDownloadTaskSubtitle(task);
    const hasSubtitle = taskHasSubtitle(task);
    const menuLabel = canDownload ? "下载字幕" : (hasSubtitle ? "字幕已在任务列表中" : "无法下载字幕");
    const menuNote = hasSubtitle
      ? "对应字幕任务已经存在。"
      : (taskSubtitleContent(task)
        ? "将使用当前页面中的台词补下载字幕。"
        : "此历史任务未保存台词文本，请先打开对应页面。");
    elements.taskContextMenu.innerHTML = `
      <button type="button" role="menuitem" data-task-context-action="download-subtitle" data-task-id="${escapeHTML(task.id)}"${canDownload ? "" : " disabled"}>
        ${icon("file-text")}<span>${menuLabel}</span>
      </button>
      ${canDownload ? "" : `<p class="task-context-note">${menuNote}</p>`}`;
    elements.taskContextMenu.hidden = false;
    elements.taskContextMenu.style.left = "0px";
    elements.taskContextMenu.style.top = "0px";
    const rect = elements.taskContextMenu.getBoundingClientRect();
    const margin = 8;
    const left = Math.max(margin, Math.min(clientX, window.innerWidth - rect.width - margin));
    const top = Math.max(margin, Math.min(clientY, window.innerHeight - rect.height - margin));
    elements.taskContextMenu.style.left = `${left}px`;
    elements.taskContextMenu.style.top = `${top}px`;
  }

  function renderTasks() {
    const focusedTaskControl = captureTaskControlFocus();
    const allTasks = [...state.tasks];
    const batches = buildTaskBatches();
    const activeTasks = allTasks.filter((task) => task.status === "running" || task.status === "queued");
    const completedTasks = allTasks.filter((task) => task.status === "completed");
    const activeBatchCount = batches.filter((batch) => summarizeBatch(batch).active > 0).length;
    const speed = activeTasks.reduce((sum, task) => sum + task.speed, 0);
    elements.taskCount.textContent = String(batches.length);
    elements.headerTaskCount.textContent = String(batches.length);
    elements.clearCompletedButton.hidden = completedTasks.length === 0;
    elements.taskOverview.hidden = allTasks.length === 0;
    if (allTasks.length) {
      elements.taskOverview.innerHTML = `
        <div class="overview-stat"><strong>${activeBatchCount}</strong><span>进行中批次</span></div>
        <div class="overview-stat"><strong>${completedTasks.length} / ${allTasks.length}</strong><span>语音完成</span></div>
        <div class="overview-stat"><strong>${formatBytes(speed)}/s</strong><span>当前速度</span></div>`;
    }

    const activeBatch = batches.find((batch) => batch.id === state.activeBatchId);
    if (state.activeBatchId && !activeBatch) {
      state.activeBatchId = "";
      state.taskStatusFilter = "all";
      state.taskPage = 1;
    }
    if (activeBatch) renderBatchDetail(activeBatch);
    else renderBatchList(batches);
    restoreTaskControlFocus(focusedTaskControl);
  }

  function renderDirectory() {
    const path = state.downloadDirectory || "尚未选择下载目录";
    elements.directoryPath.textContent = path;
    elements.directoryPath.title = state.downloadDirectory;
  }

  async function downloadBatchSubtitles(batch, button) {
    if (!batch || !state.api) return;
    const subtitleTasks = batch.tasks.filter(canDownloadTaskSubtitle);
    if (!subtitleTasks.length) {
      showToast("没有可下载的字幕", "这个批次的字幕已经存在，或任务没有保存台词文本。", "warning");
      return;
    }
    button.disabled = true;
    let created = 0;
    const errors = [];
    try {
      for (const task of subtitleTasks) {
        try {
          const result = await state.api.DownloadTaskSubtitle(task.id);
          if (result && typeof result === "object") {
            upsertTask(result, false);
            created += 1;
          }
        } catch (error) {
          errors.push(displayText(error?.message || error, `${task.fileName} 下载失败`));
        }
      }
      renderTasks();
      if (created > 0) {
        const suffix = errors.length ? `，另有 ${errors.length} 条未创建` : "";
        showToast("已创建字幕下载任务", `共 ${created} 个字幕文件${suffix}，会保存到对应语音文件夹。`, "success");
      } else {
        showToast("字幕下载失败", errors[0] || "没有成功创建字幕任务。", "error");
      }
    } finally {
      if (button.isConnected) button.disabled = false;
    }
  }

  async function runTaskAction(action, id, button) {
    const task = state.taskById.get(id);
    if (!task || !state.api) return;
    if (action === "download-subtitle" && !task.content) {
      const variant = taskSubtitleVariant(task);
      if (variant) {
        queueDownloads([variant], {
          textOnly: true,
          button,
          directory: task.directory,
          roleName: "",
        });
      }
      return;
    }
    const methods = {
      pause: "PauseTask",
      resume: "ResumeTask",
      retry: "RetryTask",
      cancel: "CancelTask",
      remove: "RemoveTask",
      open: "OpenTaskFolder",
      "download-subtitle": "DownloadTaskSubtitle",
    };
    const method = methods[action];
    if (!method || typeof state.api[method] !== "function") return;
	const startingStatus = task.status;
    button.disabled = true;
    try {
      const result = await state.api[method](id);
      if (result && typeof result === "object") upsertTask(result);
      if (action === "download-subtitle") {
        showToast("已创建字幕下载任务", "字幕会保存到对应音频的同一文件夹。", "success");
      }
	  // 后端事件可能比命令返回更快；只在尚未收到新状态时做乐观更新。
	  if (task.status === startingStatus) {
		if (action === "pause") task.status = "paused";
      if (action === "resume" || action === "retry") {
        task.status = "queued";
        task.error = "";
        task.speed = 0;
      }
		if (action === "cancel") task.status = "cancelled";
	  }
      if (action === "remove") {
        state.taskById.delete(id);
        state.tasks = state.tasks.filter((item) => item.id !== id);
      }
      renderTasks();
    } catch (error) {
      const labels = { pause: "暂停", resume: "继续", retry: "重试", cancel: "取消", remove: "移除", open: "打开文件夹", "download-subtitle": "下载字幕" };
      showToast(`${labels[action] || "操作"}失败`, displayText(error?.message || error, "请稍后重试。"), "error");
    } finally {
      if (button.isConnected) button.disabled = false;
    }
  }

  async function clearCompletedTasks() {
    if (!state.api) return;
    elements.clearCompletedButton.disabled = true;
    try {
      await state.api.ClearCompletedTasks();
      state.tasks = state.tasks.filter((task) => task.status !== "completed");
      state.taskById = new Map(state.tasks.map((task) => [task.id, task]));
      renderTasks();
    } catch (error) {
      showToast("清除失败", displayText(error?.message || error, "无法清除已完成任务。"), "error");
    } finally {
      elements.clearCompletedButton.disabled = false;
    }
  }

  function showToast(title, message, type = "info", duration = 3800) {
    const toast = document.createElement("div");
    const iconName = type === "success" ? "check" : type === "error" || type === "warning" ? "alert" : "info";
    toast.className = `toast ${type}`;
    toast.innerHTML = `${icon(iconName)}<div><strong>${escapeHTML(title)}</strong><span>${escapeHTML(message)}</span></div><button class="icon-button" type="button" aria-label="关闭提示">${icon("x")}</button>`;
    elements.toastRegion.appendChild(toast);
    const close = () => {
      if (!toast.isConnected || toast.classList.contains("is-leaving")) return;
      toast.classList.add("is-leaving");
      window.setTimeout(() => toast.remove(), 190);
    };
    toast.querySelector("button").addEventListener("click", close);
    window.setTimeout(close, duration);
  }

  function formatVersionLabel(value) {
    const text = displayText(value);
    return text ? `v${text.replace(/^v/i, "")}` : "未知版本";
  }

  function renderUpdateState() {
    elements.currentVersionValue.textContent = formatVersionLabel(state.currentVersion);
    elements.downloadUpdateButton.hidden = !state.updateInfo?.updateAvailable || !state.updateInfo?.downloadUrl;
    if (state.updateInfo?.updateAvailable) {
      elements.updateStatus.className = "update-status is-available";
      elements.updateStatus.textContent = `发现新版本 ${formatVersionLabel(state.updateInfo.latestVersion)}，点击“下载新版本”获取安装包。`;
    } else if (state.updateError) {
      elements.updateStatus.className = "update-status is-error";
      elements.updateStatus.textContent = state.updateError;
    } else if (state.updateChecking) {
      elements.updateStatus.className = "update-status";
      elements.updateStatus.textContent = "正在检查 GitHub Release…";
    } else if (state.updateInfo) {
      elements.updateStatus.className = "update-status";
      elements.updateStatus.textContent = `当前已是最新版本 ${formatVersionLabel(state.updateInfo.latestVersion)}。`;
    } else {
      elements.updateStatus.className = "update-status";
      elements.updateStatus.textContent = "尚未检查更新。";
    }
  }

  function renderSettings() {
    const concurrency = Number(state.settings.downloadConcurrency) || 4;
    elements.concurrencyInput.value = String(concurrency);
    elements.autoUpdateCheckbox.checked = state.settings.autoCheckUpdates !== false;
    renderUpdateState();
  }

  function setActiveView(view) {
    const next = view === "settings" ? "settings" : "download";
    state.view = next;
    elements.downloadView.hidden = next !== "download";
    elements.settingsView.hidden = next !== "settings";
    document.querySelectorAll("[data-view-tab]").forEach((button) => {
      const active = button.dataset.viewTab === next;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-selected", String(active));
    });
    if (next === "settings") {
      toggleDrawer(false);
      renderSettings();
    }
  }

  async function saveSettings() {
    if (!state.api?.SaveSettings) return;
    const downloadConcurrency = Number(elements.concurrencyInput.value);
    if (!Number.isInteger(downloadConcurrency) || downloadConcurrency < 1 || downloadConcurrency > 32) {
      showToast("设置无效", "下载并发数必须是 1 到 32 之间的整数。", "warning");
      return;
    }
    elements.saveSettingsButton.disabled = true;
    try {
      const result = await state.api.SaveSettings({
        downloadConcurrency,
        autoCheckUpdates: elements.autoUpdateCheckbox.checked,
      });
      state.settings = {
        downloadConcurrency: Number(result?.downloadConcurrency) || downloadConcurrency,
        autoCheckUpdates: result?.autoCheckUpdates !== false,
      };
      elements.settingsSaveStatus.textContent = "已保存并立即生效";
      showToast("设置已保存", `当前同时下载 ${state.settings.downloadConcurrency} 个文件。`, "success");
      renderSettings();
      window.setTimeout(() => { if (elements.settingsSaveStatus) elements.settingsSaveStatus.textContent = ""; }, 2600);
    } catch (error) {
      showToast("保存设置失败", displayText(error?.message || error, "无法应用下载并发设置。"), "error");
    } finally {
      elements.saveSettingsButton.disabled = false;
    }
  }

  async function checkForUpdates(silent = false) {
    if (!state.api?.CheckForUpdates || state.updateChecking) return;
    state.updateChecking = true;
    state.updateError = "";
    renderUpdateState();
    elements.checkUpdateButton.disabled = true;
    try {
      state.updateInfo = await state.api.CheckForUpdates();
      if (!silent && state.updateInfo?.updateAvailable) {
        showToast("发现新版本", `${formatVersionLabel(state.updateInfo.latestVersion)} 已发布，可在设置页下载。`, "success");
      } else if (!silent) {
        showToast("已是最新版本", `当前版本 ${formatVersionLabel(state.currentVersion)} 无需更新。`, "info");
      }
    } catch (error) {
      state.updateInfo = null;
      state.updateError = displayText(error?.message || error, "检查 GitHub 更新失败，请稍后重试。");
      if (!silent) showToast("检查更新失败", state.updateError, "error");
    } finally {
      state.updateChecking = false;
      elements.checkUpdateButton.disabled = false;
      renderUpdateState();
    }
  }

  async function openUpdateDownload() {
    const url = state.updateInfo?.downloadUrl || state.updateInfo?.releaseUrl;
    if (!url || !state.api?.OpenExternalURL) return;
    try {
      await state.api.OpenExternalURL(url);
    } catch (error) {
      showToast("打开下载失败", displayText(error?.message || error, "无法打开 GitHub 下载链接。"), "error");
    }
  }

  function bindEvents() {
    document.querySelectorAll("[data-view-tab]").forEach((button) => {
      button.addEventListener("click", () => setActiveView(button.dataset.viewTab));
    });
    elements.saveSettingsButton.addEventListener("click", saveSettings);
    elements.checkUpdateButton.addEventListener("click", () => checkForUpdates(false));
    elements.downloadUpdateButton.addEventListener("click", openUpdateDownload);
    elements.autoUpdateCheckbox.addEventListener("change", () => {
      state.settings.autoCheckUpdates = elements.autoUpdateCheckbox.checked;
    });

    elements.urlForm.addEventListener("submit", (event) => {
      event.preventDefault();
      parsePage(elements.urlInput.value);
    });
    elements.urlInput.addEventListener("input", updateInputClearButton);
    elements.clearUrlButton.addEventListener("click", () => {
      elements.urlInput.value = "";
      updateInputClearButton();
      elements.urlInput.focus();
    });

    elements.languageTrigger.addEventListener("click", () => toggleLanguageMenu());
    elements.languageMenu.addEventListener("change", (event) => {
      const input = event.target.closest("[data-language-code]");
      if (!input) return;
      if (input.checked) state.languageCodes.add(input.dataset.languageCode);
      else state.languageCodes.delete(input.dataset.languageCode);
      renderLanguageMenu();
      renderResults();
    });
    elements.languageMenu.addEventListener("click", (event) => {
      const action = event.target.closest("[data-language-action]")?.dataset.languageAction;
      const only = event.target.closest("[data-only-language]")?.dataset.onlyLanguage;
      if (action === "all") state.languageCodes = new Set(state.page?.languages.map((language) => language.code) || []);
      if (action === "none") state.languageCodes.clear();
      if (only) state.languageCodes = new Set([only]);
      if (action || only) {
        renderLanguageMenu();
        renderResults();
      }
    });

    elements.sceneTrigger.addEventListener("click", () => toggleSceneMenu());
    elements.sceneMenu.addEventListener("change", (event) => {
      const input = event.target.closest("[data-scene-key]");
      if (!input) return;
      if (input.checked) state.sceneKeys.add(input.dataset.sceneKey);
      else state.sceneKeys.delete(input.dataset.sceneKey);
      renderSceneMenu();
      renderResults();
    });
    elements.sceneMenu.addEventListener("click", (event) => {
      const action = event.target.closest("[data-scene-action]")?.dataset.sceneAction;
      const only = event.target.closest("[data-only-scene]")?.dataset.onlyScene;
      if (action === "all") state.sceneKeys = new Set(state.page?.scenes.map((scene) => scene.key) || []);
      if (action === "none") state.sceneKeys.clear();
      if (only) state.sceneKeys = new Set([only]);
      if (action || only) {
        renderSceneMenu();
        renderResults();
      }
    });

    elements.searchInput.addEventListener("input", debounce(() => {
      state.query = elements.searchInput.value;
      renderResults();
    }, 110));

    elements.selectAllCheckbox.addEventListener("change", () => {
      const variants = visibleVariants();
      const allSelected = variants.length > 0 && variants.every((variant) => state.selected.has(variant._key));
      variants.forEach((variant) => {
        if (allSelected) state.selected.delete(variant._key);
        else state.selected.set(variant._key, variant);
      });
      renderResults();
    });

    elements.voiceList.addEventListener("change", (event) => {
      const variantKey = event.target.dataset.variantSelect;
      const entryId = event.target.dataset.entrySelect;
      if (variantKey) {
        const variant = state.allVariants.get(variantKey);
        if (variant) {
          if (event.target.checked) state.selected.set(variantKey, variant);
          else state.selected.delete(variantKey);
        }
        renderResults();
        return;
      }
      if (entryId) {
        const group = filteredGroups().find(({ entry }) => entry.id === entryId);
        if (!group) return;
        const allSelected = group.variants.every((variant) => state.selected.has(variant._key));
        group.variants.forEach((variant) => {
          if (allSelected) state.selected.delete(variant._key);
          else state.selected.set(variant._key, variant);
        });
        renderResults();
      }
    });

    elements.voiceList.addEventListener("click", (event) => {
      const target = event.target.closest("[data-action]");
      if (!target) return;
      const action = target.dataset.action;
      const key = target.dataset.key;
      if (action === "play") toggleAudio(key);
      if (action === "copy") copyTranscript(key);
      if (action === "download-text") {
        const variant = state.allVariants.get(key);
        if (variant) queueDownloads([variant], { textOnly: true, button: target });
      }
      if (action === "download-one") {
        const variant = state.allVariants.get(key);
        if (variant) queueDownloads([variant], { button: target });
      }
      if (action === "download-entry") {
        const group = filteredGroups().find(({ entry }) => entry.id === target.dataset.entryId);
        if (group) queueDownloads(group.variants, { button: target });
      }
    });

    elements.clearSelection.addEventListener("click", () => {
      state.selected.clear();
      renderResults();
    });
    elements.clearHiddenSelection.addEventListener("click", () => {
      const keys = new Set(visibleVariants().map((variant) => variant._key));
      [...state.selected.keys()].forEach((key) => {
        if (!keys.has(key)) state.selected.delete(key);
      });
      renderResults();
    });
    elements.batchDownloadButton.addEventListener("click", () => queueDownloads([...state.selected.values()], {
      button: elements.batchDownloadButton,
      clearSelection: true,
    }));
    elements.batchSubtitleDownloadButton.addEventListener("click", () => queueDownloads([...state.selected.values()], {
      textOnly: true,
      button: elements.batchSubtitleDownloadButton,
      clearSelection: true,
    }));

    elements.includeSubtitlesCheckbox.addEventListener("change", () => {
      state.includeSubtitles = elements.includeSubtitlesCheckbox.checked;
    });

    elements.chooseDirectoryButton.addEventListener("click", chooseDirectory);
    elements.clearCompletedButton.addEventListener("click", clearCompletedTasks);
    elements.taskPanel.addEventListener("click", (event) => {
      const batchAction = event.target.closest("[data-batch-action]");
      if (batchAction) {
        const batch = buildTaskBatches().find((item) => item.id === batchAction.dataset.batchId);
        if (batch && batchAction.dataset.batchAction === "download-subtitles") {
          downloadBatchSubtitles(batch, batchAction);
        }
        return;
      }
      const batchToggle = event.target.closest("[data-batch-toggle]");
      if (batchToggle) {
        state.activeBatchId = batchToggle.dataset.batchToggle;
        state.taskStatusFilter = "all";
        state.taskPage = 1;
        renderTasks();
        window.requestAnimationFrame(() => focusWithoutScroll(elements.batchBackButton));
        return;
      }
      if (event.target.closest("#batchBackButton")) {
        const previousBatchId = state.activeBatchId;
        state.activeBatchId = "";
        state.taskStatusFilter = "all";
        state.taskPage = 1;
        renderTasks();
        window.requestAnimationFrame(() => {
          const target = [...elements.taskList.querySelectorAll("[data-batch-toggle]")]
            .find((button) => button.dataset.batchToggle === previousBatchId);
          focusWithoutScroll(target);
        });
        return;
      }
      const statusButton = event.target.closest("[data-batch-status]");
      if (statusButton) {
        state.taskStatusFilter = statusButton.dataset.batchStatus;
        state.taskPage = 1;
        renderTasks();
        return;
      }
      const pageButton = event.target.closest("[data-batch-page]");
      if (pageButton) {
        state.taskPage += pageButton.dataset.batchPage === "next" ? 1 : -1;
        renderTasks();
        return;
      }
      const button = event.target.closest("[data-task-action]");
      if (button) runTaskAction(button.dataset.taskAction, button.dataset.taskId, button);
    });
    elements.taskPanel.addEventListener("contextmenu", (event) => {
      const card = event.target.closest("[data-task-card]");
      if (!card) return;
      const task = state.taskById.get(card.dataset.taskCard);
      if (!task || task.status !== "completed" || task.type !== "audio") return;
      event.preventDefault();
      openTaskContextMenu(task, event.clientX, event.clientY);
    });
    elements.taskContextMenu.addEventListener("click", (event) => {
      const button = event.target.closest("[data-task-context-action]");
      if (!button || button.disabled) return;
      const action = button.dataset.taskContextAction;
      const id = button.dataset.taskId;
      const task = state.taskById.get(id);
      closeTaskContextMenu();
      if (action === "download-subtitle" && task && !task.content) {
        const variant = taskSubtitleVariant(task);
        if (variant) {
          queueDownloads([variant], {
            textOnly: true,
            button,
            directory: task.directory,
            roleName: "",
          });
          return;
        }
      }
      runTaskAction(action, id, button);
    });

    elements.taskDrawerTrigger.addEventListener("click", () => toggleDrawer());
    elements.closeTaskDrawer.addEventListener("click", () => toggleDrawer(false));
    elements.drawerScrim.addEventListener("click", () => toggleDrawer(false));

    document.addEventListener("click", (event) => {
      const eventPath = typeof event.composedPath === "function" ? event.composedPath() : [];
      const insideLanguagePicker = eventPath.includes(elements.languagePicker) || elements.languagePicker.contains(event.target);
      const insideScenePicker = eventPath.includes(elements.scenePicker) || elements.scenePicker.contains(event.target);
      if (state.languageMenuOpen && !insideLanguagePicker) toggleLanguageMenu(false);
      if (state.sceneMenuOpen && !insideScenePicker) toggleSceneMenu(false);
      if (!elements.taskContextMenu.contains(event.target)) closeTaskContextMenu();
    });
    document.addEventListener("keydown", (event) => {
	  if (event.key === "Tab" && state.drawerOpen && window.matchMedia("(max-width: 1120px)").matches) {
		const focusable = drawerFocusableElements();
		if (focusable.length) {
		  const first = focusable[0];
		  const last = focusable[focusable.length - 1];
		  if (event.shiftKey && document.activeElement === first) {
			event.preventDefault();
			last.focus();
		  } else if (!event.shiftKey && document.activeElement === last) {
			event.preventDefault();
			first.focus();
		  }
		}
      }
      if (event.key === "Escape") {
        if (!elements.taskContextMenu.hidden) {
          event.preventDefault();
          closeTaskContextMenu();
          return;
        }
        if (state.languageMenuOpen) {
          event.preventDefault();
          toggleLanguageMenu(false);
          focusWithoutScroll(elements.languageTrigger);
          return;
        }
        if (state.sceneMenuOpen) {
          event.preventDefault();
          toggleSceneMenu(false);
          focusWithoutScroll(elements.sceneTrigger);
          return;
        }
        if (state.drawerOpen) toggleDrawer(false);
      }
      if ((event.ctrlKey || event.metaKey) && event.key.toLocaleLowerCase() === "k" && state.page) {
        event.preventDefault();
        elements.searchInput.focus();
        elements.searchInput.select();
      }
    });
    window.addEventListener("resize", () => {
      closeTaskContextMenu();
      if (!window.matchMedia("(max-width: 1120px)").matches && state.drawerOpen) toggleDrawer(false);
	  else syncDrawerAccessibility();
    });
    window.addEventListener("beforeunload", stopAudio);
  }

  async function loadBootstrap() {
    try {
      const bootstrap = await state.api.GetBootstrap();
      state.downloadDirectory = displayText(bootstrap?.downloadDirectory);
      state.currentVersion = displayText(bootstrap?.version);
      const rawSettings = bootstrap?.settings || {};
      state.settings = {
        downloadConcurrency: Number(rawSettings.downloadConcurrency) || 4,
        autoCheckUpdates: rawSettings.autoCheckUpdates !== false,
      };
      state.tasks = [];
      state.taskById.clear();
      unwrapTasks(bootstrap?.tasks || []).forEach((task) => upsertTask(task, false));
      if (bootstrap?.version) elements.versionBadge.textContent = `v${String(bootstrap.version).replace(/^v/i, "")}`;
      renderDirectory();
      renderSettings();
      renderTasks();
    } catch (error) {
      showToast("初始化失败", displayText(error?.message || error, "无法读取应用配置。"), "error", 5200);
    }
  }

  function subscribeTaskEvents() {
    if (PREVIEW_MODE || typeof window.runtime?.EventsOn !== "function") return;
    window.runtime.EventsOn("download:task", (...args) => {
      const payload = args.length === 1 ? args[0] : args.at(-1);
      if (Array.isArray(payload) && payload.length === 1) upsertTask(payload[0]);
      else upsertTask(payload);
    });
  }

  function setBridgeAvailability(available) {
    elements.bridgeBanner.hidden = available || PREVIEW_MODE;
    elements.parseButton.disabled = !available;
    elements.chooseDirectoryButton.disabled = !available;
    if (!available) elements.urlHint.textContent = "当前是浏览器预览；解析与下载需要在“拾音”应用中运行";
  }

  function createPreviewData() {
    return {
      title: "米雪儿·李 / 语音台词",
      sourceUrl: EXAMPLE_URL,
      languages: [
        { code: "zh-CN", name: "中文", count: 5 },
        { code: "ja-JP", name: "日语", count: 5 },
        { code: "en-US", name: "英语", count: 5 },
      ],
      warnings: ["“节日问候”中的一条英语语音未提供台词文本，仍可正常试听和下载。"],
      entries: [
        {
          id: "greeting-01", category: "卡拉彼丘", title: "初次见面，很高兴认识你",
          variants: [
            { id: "greeting-01-zh", language: "zh-CN", languageName: "中文", transcript: "你好，我是米雪儿·李。接下来的旅程，请多指教啦。", fileName: "初次见面_中文.mp3", url: "https://example.com/audio/greeting-zh.mp3" },
            { id: "greeting-01-ja", language: "ja-JP", languageName: "日语", transcript: "はじめまして、ミシェル・リーです。これからよろしくね。", fileName: "初次见面_日本語.mp3", url: "https://example.com/audio/greeting-ja.mp3" },
            { id: "greeting-01-en", language: "en-US", languageName: "英语", transcript: "Nice to meet you. I'm Michelle Lee. Let's make this journey count.", fileName: "初次见面_English.mp3", url: "https://example.com/audio/greeting-en.mp3" },
          ],
        },
        {
          id: "battle-01", category: "宿舍", title: "准备出发",
          variants: [
            { id: "battle-01-zh", language: "zh-CN", languageName: "中文", transcript: "准备好了吗？那就跟紧我，我们出发！", fileName: "准备出发_中文.mp3", url: "https://example.com/audio/battle-zh.mp3" },
            { id: "battle-01-ja", language: "ja-JP", languageName: "日语", transcript: "準備はいい？ 私についてきて、出発するよ！", fileName: "准备出发_日本語.mp3", url: "https://example.com/audio/battle-ja.mp3" },
            { id: "battle-01-en", language: "en-US", languageName: "英语", transcript: "Ready? Stay close—we're moving out!", fileName: "准备出发_English.mp3", url: "https://example.com/audio/battle-en.mp3" },
          ],
        },
        {
          id: "idle-01", category: "宿舍", title: "关于音乐",
          variants: [
            { id: "idle-01-zh", language: "zh-CN", languageName: "中文", transcript: "听见了吗？风也在哼着今天的旋律。", fileName: "关于音乐_中文.mp3", url: "https://example.com/audio/music-zh.mp3" },
            { id: "idle-01-ja", language: "ja-JP", languageName: "日语", transcript: "聞こえる？ 風も今日のメロディーを歌っているよ。", fileName: "关于音乐_日本語.mp3", url: "https://example.com/audio/music-ja.mp3" },
            { id: "idle-01-en", language: "en-US", languageName: "英语", transcript: "Can you hear it? Even the wind is humming today's melody.", fileName: "关于音乐_English.mp3", url: "https://example.com/audio/music-en.mp3" },
          ],
        },
        {
          id: "victory-01", category: "对局", title: "战斗胜利",
          variants: [
            { id: "victory-01-zh", language: "zh-CN", languageName: "中文", transcript: "漂亮的配合！这场胜利属于我们每一个人。", fileName: "战斗胜利_中文.mp3", url: "https://example.com/audio/victory-zh.mp3" },
            { id: "victory-01-ja", language: "ja-JP", languageName: "日语", transcript: "いい連携だったね！ この勝利はみんなのものだよ。", fileName: "战斗胜利_日本語.mp3", url: "https://example.com/audio/victory-ja.mp3" },
            { id: "victory-01-en", language: "en-US", languageName: "英语", transcript: "Great teamwork! This victory belongs to every one of us.", fileName: "战斗胜利_English.mp3", url: "https://example.com/audio/victory-en.mp3" },
          ],
        },
        {
          id: "festival-01", category: "宿舍", title: "夏日庆典",
          variants: [
            { id: "festival-01-zh", language: "zh-CN", languageName: "中文", transcript: "烟花马上就要开始了，我们去找个视野最好的位置吧。", fileName: "夏日庆典_中文.mp3", url: "https://example.com/audio/festival-zh.mp3" },
            { id: "festival-01-ja", language: "ja-JP", languageName: "日语", transcript: "もうすぐ花火が始まるよ。一番よく見える場所を探そう。", fileName: "夏日庆典_日本語.mp3", url: "https://example.com/audio/festival-ja.mp3" },
            { id: "festival-01-en", language: "en-US", languageName: "英语", transcript: "", fileName: "夏日庆典_English.mp3", url: "https://example.com/audio/festival-en.mp3" },
          ],
        },
      ],
    };
  }

  function createPreviewAPI() {
    const batchId = "preview-batch-main";
    const createdAt = "2026-08-03T08:30:00Z";
    const statuses = ["queued", "running", "paused", "completed", "failed", "cancelled"];
    const tasks = Array.from({ length: 27 }, (_, index) => {
      const status = statuses[index % statuses.length];
      const totalBytes = 7200000 + index * 210000;
      const ratios = { queued: 0, running: 0.46, paused: 0.72, completed: 1, failed: 0.19, cancelled: 0.08 };
      return {
        id: `preview-task-${index + 1}`,
        batchId,
        createdAt,
        title: ["初次见面，很高兴认识你", "准备出发", "关于音乐", "战斗胜利"][index % 4],
        category: index % 2 ? "对局" : "宿舍",
        fileName: `米雪儿语音-${String(index + 1).padStart(3, "0")}.mp3`,
        content: index % 3 === 2 ? "" : "这是预览模式中的示例台词。",
        languageName: ["中文", "日语", "英语"][index % 3],
        status,
        downloadedBytes: Math.round(totalBytes * ratios[status]),
        totalBytes: status === "queued" && index > 18 ? 0 : totalBytes,
        speedBytesPerSecond: status === "running" ? 820000 + index * 24000 : 0,
        error: status === "failed" ? "连接中断，已保留断点数据，可直接重试。" : "",
      };
    });
    tasks.push({
      id: "preview-legacy-task",
      title: "历史单条下载",
      category: "宿舍",
      fileName: "历史语音-001.mp3",
      languageName: "中文",
      status: "completed",
      downloadedBytes: 6400000,
      totalBytes: 6400000,
    });
    return {
      async GetBootstrap() { return { downloadDirectory: "D:\\拾音\\米雪儿·李", tasks, version: "1.0.0-preview" }; },
      async ParsePage() { await new Promise((resolve) => window.setTimeout(resolve, 650)); return { ok: true, page: createPreviewData() }; },
      async ChooseDownloadDirectory() { return "D:\\拾音\\米雪儿·李"; },
      async QueueDownloads(request) {
        const queuedBatchId = `preview-batch-${Date.now()}`;
        const queuedAt = new Date().toISOString();
        return request.items.map((item, index) => ({
          id: `preview-${Date.now()}-${index}`,
          batchId: queuedBatchId,
          createdAt: queuedAt,
          ...item,
          status: "queued",
          downloadedBytes: 0,
          totalBytes: 7200000 + index * 430000,
        }));
      },
      async PauseTask() {},
      async ResumeTask() {},
      async RetryTask() {},
      async CancelTask() {},
      async RemoveTask() {},
      async ClearCompletedTasks() {},
      async DownloadTaskSubtitle(id) {
        const source = tasks.find((task) => task.id === id);
        if (!source?.content) throw new Error("此历史任务未保存台词文本。");
        return {
          ...source,
          id: `preview-subtitle-${Date.now()}`,
          type: "text",
          fileName: subtitleFileName(source.fileName),
          content: source.content,
          status: "queued",
          downloadedBytes: 0,
          totalBytes: source.content.length,
        };
      },
      async OpenTaskFolder() { showToast("预览模式", "在桌面应用中会打开文件所在目录。", "info"); },
      async SaveSettings(request) { return { downloadConcurrency: Number(request?.downloadConcurrency) || 4, autoCheckUpdates: request?.autoCheckUpdates !== false }; },
      async CheckForUpdates() { return { currentVersion: "1.0.0-preview", latestVersion: "1.0.0-preview", updateAvailable: false, releaseUrl: "", downloadUrl: "" }; },
      async OpenExternalURL() { showToast("预览模式", "桌面应用中会打开 GitHub 下载链接。", "info"); },
    };
  }

  async function init() {
    cacheElements();
    bindEvents();
    elements.searchInput.parentElement.querySelector("kbd").textContent = /Mac|iPhone|iPad/.test(navigator.platform) ? "⌘ K" : "Ctrl K";
    updateInputClearButton();
    renderDirectory();
    renderTasks();
	syncDrawerAccessibility();

    state.api = PREVIEW_MODE ? createPreviewAPI() : window.go?.main?.App;
    setBridgeAvailability(Boolean(state.api));
    if (!state.api) return;
    await loadBootstrap();
    subscribeTaskEvents();
    setActiveView("download");
    if (state.settings.autoCheckUpdates) {
      window.setTimeout(() => checkForUpdates(true), 900);
    }

    if (PREVIEW_MODE) {
      document.body.classList.add("preview-mode");
      elements.urlInput.value = EXAMPLE_URL;
      updateInputClearButton();
      state.phase = "success";
      setPage(createPreviewData());
      renderParseFeedback("success", { title: "静态预览数据", message: "已载入中、日、英三语示例，可直接体验筛选、选择与任务操作。" });
    }
  }

  document.addEventListener("DOMContentLoaded", init);
})();
