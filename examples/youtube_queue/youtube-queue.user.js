// ==UserScript==
// @name        YouTube 投递到 GoQueue 队列
// @namespace   https://github.com/deepseekretro/go-queue-sqlite
// @version     1.6.0
// @description 在 YouTube 首页/频道/搜索页/播放页添加「加入队列」按钮，点击后将视频信息投递到 GoQueue 任务队列
// @author      GoQueue
// @match       https://www.youtube.com/
// @match       https://www.youtube.com/?*
// @match       https://www.youtube.com/@*/search*
// @match       https://www.youtube.com/@*/videos*
// @match       https://www.youtube.com/results?search_query=*
// @match       https://www.youtube.com/watch*
// @grant       GM_xmlhttpRequest
// @grant       GM_addStyle
// @grant       GM_getValue
// @grant       GM_setValue
// @connect     localhost
// @connect     127.0.0.1
// @connect     *
// @run-at      document-idle
// ==/UserScript==

(function () {
  'use strict';

  // ─── ⚙️ 配置区域 ──────────────────────────────────────────────────────────
  //
  // GoQueue 服务地址，默认本地开发环境。
  // 生产环境请修改为实际部署地址，例如：
  //   const GOQUEUE_HOST = 'https://your-server.example.com';
  //
  const GOQUEUE_HOST = (GM_getValue('goqueue_host', 'http://localhost:8080') || 'http://localhost:8080').replace(/\/+$/, '');

  // 投递到哪个队列
  const QUEUE_NAME   = GM_getValue('goqueue_queue', 'youtube');

  // job_type：Worker 侧用于路由的任务类型标识
  // Worker 只需注册 'process_youtube_video' handler 即可处理
  const JOB_TYPE     = 'process_youtube_video';

  // 任务优先级（1-10，越大越优先）
  const JOB_PRIORITY = 5;

  // 失败后最多重试次数
  const MAX_ATTEMPTS = 3;

  // ─── 🎨 样式注入 ──────────────────────────────────────────────────────────
  const css = `
    .goqueue-btn {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        background-color: #f2f2f2;
        color: #0f0f0f;
        border: none;
        border-radius: 18px;
        padding: 0 12px;
        height: 32px;
        font-family: "Roboto","Arial",sans-serif;
        font-size: 14px;
        font-weight: 500;
        cursor: pointer;
        text-decoration: none;
        transition: background-color 0.2s cubic-bezier(0.05, 0, 0, 1);
        margin-top: 6px;
        position: relative;
        z-index: 100;
        user-select: none;
    }
    .goqueue-btn svg {
        margin-right: 6px;
        fill: currentColor;
        width: 18px;
        height: 18px;
        pointer-events: none;
    }
    .goqueue-btn:hover  { background-color: #e5e5e5; }
    .goqueue-btn:active { background-color: #d9d9d9; }

    html[dark] .goqueue-btn {
        background-color: rgba(255, 255, 255, 0.1);
        color: #f1f1f1;
    }
    html[dark] .goqueue-btn:hover { background-color: rgba(255, 255, 255, 0.2); }

    .goqueue-watch-button {
        height: 36px !important;
        background-color: #272727 !important;
        color: white !important;
        margin-left: 8px;
    }
    .goqueue-watch-button:hover { background-color: #3f3f3f !important; }

    /* 已加入队列状态 */
    .goqueue-btn.goqueue-queued {
        background-color: #00b300 !important;
        color: white !important;
        cursor: default;
    }
    .goqueue-btn.goqueue-queued:hover { background-color: #00b300 !important; }

    #goqueue-toast {
        position: fixed;
        top: 60px;
        left: 50%;
        transform: translateX(-50%);
        padding: 10px 24px;
        border-radius: 4px;
        font-size: 14px;
        color: white;
        background-color: #0f0f0f;
        z-index: 99999;
        display: none;
        box-shadow: 0 2px 10px rgba(0,0,0,0.3);
        opacity: 0;
        transition: opacity 0.3s;
    }
  `;

  if (typeof GM_addStyle !== 'undefined') {
    GM_addStyle(css);
  } else {
    const styleEl = document.createElement('style');
    styleEl.textContent = css;
    document.head.appendChild(styleEl);
  }

  // ─── 🛠️ 辅助工具 ──────────────────────────────────────────────────────────
  function createQueueIcon() {
    const svgNS = "http://www.w3.org/2000/svg";
    const svg = document.createElementNS(svgNS, "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("preserveAspectRatio", "xMidYMid meet");
    svg.setAttribute("focusable", "false");
    const path = document.createElementNS(svgNS, "path");
    // 队列/加入图标（+ 号变体）
    path.setAttribute("d", "M19 3H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm-2 10h-4v4h-2v-4H7v-2h4V7h2v4h4v2z");
    svg.appendChild(path);
    return svg;
  }

  function showToast(message, success = true) {
    let notifier = document.getElementById('goqueue-toast');
    if (!notifier) {
      notifier = document.createElement('div');
      notifier.id = 'goqueue-toast';
      document.body.appendChild(notifier);
    }
    notifier.textContent = message;
    notifier.style.backgroundColor = success ? '#0f0f0f' : '#cc0000';
    notifier.style.display = 'block';
    requestAnimationFrame(() => { notifier.style.opacity = '1'; });
    setTimeout(() => {
      notifier.style.opacity = '0';
      setTimeout(() => { notifier.style.display = 'none'; }, 300);
    }, 2500);
  }

  // ─── 📋 日志打印 ──────────────────────────────────────────────────────────
  function logVideoInfo(videoTitle, videoUrl, meta) {
    console.group('📋 GoQueue 准备投递');
    console.log('📄 标题:', videoTitle);
    console.log('🔗 链接:', videoUrl);
    if (meta) {
      if (meta.channelName) console.log('📺 频道:', meta.channelName, meta.channelUrl);
      if (meta.views)       console.log('👁 观看数:', meta.views);
      if (meta.publishedAt) console.log('🕐 发布时间:', meta.publishedAt);
    }
    console.log('📡 GoQueue API:', `${GOQUEUE_HOST}/api/jobs`);
    console.log('📦 队列:', QUEUE_NAME, '| job_type:', JOB_TYPE);
    console.groupEnd();
  }

  // ─── 📡 投递到 GoQueue ────────────────────────────────────────────────────
  //
  // 调用 POST /api/jobs 将视频信息作为任务 data 投递到队列。
  // Worker 侧只需注册 'process_youtube_video' handler 即可处理，例如：
  //
  //   JS Worker:
  //     handlers['process_youtube_video'] = async (data, tags) => {
  //       console.log('处理视频:', data.title, data.url);
  //       // ... 下载、转码、存档等业务逻辑 ...
  //       return `已处理: ${data.title}`;
  //     };
  //
  //   Python Worker:
  //     @worker.handler('process_youtube_video')
  //     async def handle(data, tags):
  //         print(f"处理视频: {data['title']}, URL: {data['url']}")
  //         # ... 业务逻辑 ...
  //         return f"已处理: {data['title']}"
  //
  function enqueueVideo(videoTitle, videoUrl, button, meta) {
    showToast('🚀 投递中...');

    const jobPayload = {
      queue:        QUEUE_NAME,
      job_type:     JOB_TYPE,
      priority:     JOB_PRIORITY,
      max_attempts: MAX_ATTEMPTS,
      tags:         ['youtube'],
      data: {
        title:        videoTitle,
        url:          videoUrl,
        views:        (meta && meta.views)       || '',
        published_at: (meta && meta.publishedAt) || '',
        channel_name: (meta && meta.channelName) || '',
        channel_url:  (meta && meta.channelUrl)  || '',
        // 额外元数据，方便 Worker 侧使用
        queued_at:    new Date().toISOString(),
        source_page:  location.href
      }
    };

    console.log('[GoQueue] 投递 payload:', JSON.stringify(jobPayload, null, 2));

    GM_xmlhttpRequest({
      method:  'POST',
      url:     `${GOQUEUE_HOST}/api/jobs`,
      headers: { 'Content-Type': 'application/json' },
      data:    JSON.stringify(jobPayload),
      onload: (res) => {
        if (res.status === 200 || res.status === 201) {
          let jobId = '';
          try {
            const resp = JSON.parse(res.responseText);
            // 服务端返回: {"job_id": 123, "queue": "youtube", "status": "pending"}
            jobId = resp.job_id ? ` #${resp.job_id}` : '';
            console.log(`✅ 投递成功，Job${jobId}`, resp);
          } catch (_) {
            console.log('✅ 投递成功，响应:', res.responseText);
          }
          showToast(`✅ 已加入队列${jobId}`);
          // 按钮变为「已加入」状态，防止重复投递
          if (button) {
            button.classList.add('goqueue-queued');
            button.disabled = true;
            button.querySelector('span').textContent = `已加入${jobId}`;
          }
        } else {
          console.error('❌ 投递失败，状态码:', res.status, '\n响应:', res.responseText);
          showToast(`❌ 投递失败: ${res.status}`, false);
        }
      },
      onerror: (err) => {
        console.error('❌ 网络错误:', err);
        showToast('❌ 网络错误，请检查 GoQueue 服务是否运行', false);
      }
    });
  }

  // ─── 🔎 标题获取器（覆盖所有已知 YouTube DOM 变体）────────────────────────
  //
  // YouTube 类名有两套风格，需同时覆盖：
  //   驼峰式（新版）: ytLockupMetadataViewModelHeadingReset
  //   连字符式（旧版）: yt-lockup-metadata-view-model__heading-reset
  //
  function getSafeTitle(context = null, isWatchPage = false) {
    let title = '';

    if (isWatchPage) {
      // 播放页：按优先级逐一尝试
      const watchSelectors = [
        'ytd-watch-metadata h1 yt-formatted-string',  // 2024+ 新版
        'ytd-watch-metadata h1',
        '#above-the-fold #title h1 yt-formatted-string',
        '#above-the-fold #title h1',
        '#title h1 yt-formatted-string',
        '#title h1',
        'h1.ytd-watch-metadata',
      ];
      for (const sel of watchSelectors) {
        const el = document.querySelector(sel);
        if (el) {
          title = el.textContent.trim();
          if (title) break;
        }
      }
      // 兜底：document.title（格式为 "视频标题 - YouTube"）
      if (!title) {
        title = document.title.replace(/\s*[-–—]\s*YouTube\s*$/i, '').trim();
      }

    } else if (context) {
      // 列表页卡片：按优先级逐一尝试

      // 1. 驼峰式类名（2025+ 新版，实测 DOM）
      //    <h3 class="ytLockupMetadataViewModelHeadingReset" title="视频标题">
      const camelHeading = context.querySelector('.ytLockupMetadataViewModelHeadingReset');
      if (camelHeading) {
        title = camelHeading.title || camelHeading.textContent.trim();
        if (title) return title;
      }

      //    <a class="ytLockupMetadataViewModelTitle" ...>
      const camelTitleLink = context.querySelector('a.ytLockupMetadataViewModelTitle');
      if (camelTitleLink) {
        title = camelTitleLink.title
             || camelTitleLink.getAttribute('aria-label')
             || camelTitleLink.textContent.trim();
        // aria-label 格式为 "标题 23 minutes"，去掉末尾时长
        if (title) return title.replace(/\s+\d+\s+(minutes?|hours?|seconds?).*$/i, '').trim();
      }

      // 2. 连字符式类名（2023-2024 版本）
      const hyphenHeading = context.querySelector('.yt-lockup-metadata-view-model__heading-reset');
      if (hyphenHeading) {
        title = hyphenHeading.title || hyphenHeading.textContent.trim();
        if (title) return title;
      }

      const hyphenTitleLink = context.querySelector('.yt-lockup-metadata-view-model__title');
      if (hyphenTitleLink) {
        title = hyphenTitleLink.title || hyphenTitleLink.textContent.trim();
        if (title) return title;
      }

      // 3. lockup wiz（某些版本）
      const wizLink = context.querySelector('.yt-lockup-view-model-wiz__metadata a');
      if (wizLink) {
        title = wizLink.title || wizLink.getAttribute('aria-label') || wizLink.textContent.trim();
        if (title) return title;
      }

      // 4. 旧版 #video-title（yt-formatted-string 或普通元素）
      const videoTitleSelectors = [
        'yt-formatted-string#video-title',
        'span#video-title',
        'a#video-title',
        '#video-title',
      ];
      for (const sel of videoTitleSelectors) {
        const el = context.querySelector(sel);
        if (el) {
          // a 标签优先取 title 属性（最干净，无换行/空格）
          title = el.title || el.textContent.trim() || el.getAttribute('aria-label') || '';
          if (title) return title;
        }
      }

      // 5. a#video-title-link
      const titleLink = context.querySelector('a#video-title-link');
      if (titleLink) {
        title = titleLink.title || titleLink.getAttribute('aria-label') || titleLink.textContent.trim();
        if (title) return title;
      }

      // 6. 通用兜底：卡片内 href 含 watch 的 <a>，优先取 title 属性
      const watchLinks = context.querySelectorAll('a[href*="watch"]');
      for (const a of watchLinks) {
        title = a.title || '';
        if (title) return title;
      }
      // aria-label 兜底（格式可能含时长，去掉）
      for (const a of watchLinks) {
        title = a.getAttribute('aria-label') || '';
        if (title) return title.replace(/\s+\d+\s+(minutes?|hours?|seconds?).*$/i, '').trim();
      }

      // 7. 最后兜底：a#thumbnail 的 aria-label
      const thumb = context.querySelector('a#thumbnail');
      if (thumb) {
        title = thumb.getAttribute('aria-label') || thumb.title || '';
        if (title) return title;
      }
    }

    return title ? title.trim() : '未找到标题';
  }

  // ─── 🔘 按钮创建 ──────────────────────────────────────────────────────────
  function createButton(urlGetter, contextOrType) {
    const button = document.createElement('button');
    button.className = 'goqueue-btn';

    button.appendChild(createQueueIcon());
    const span = document.createElement('span');
    span.textContent = '加入队列';
    button.appendChild(span);

    button.onclick = (e) => {
      e.stopPropagation();
      e.preventDefault();
      if (button.disabled) return;

      let finalUrl = '', isWatch = false;
      if (typeof urlGetter === 'function') { finalUrl = urlGetter(); isWatch = true; }
      else { finalUrl = urlGetter; isWatch = false; }

      const finalTitle = getSafeTitle(isWatch ? null : contextOrType, isWatch);

      if (finalUrl && finalUrl.includes('watch')) {
        const cardMeta = isWatch ? {} : extractCardMeta(contextOrType);
        logVideoInfo(finalTitle, finalUrl, cardMeta);
        enqueueVideo(finalTitle, finalUrl, button, cardMeta);
      } else {
        showToast('❌ 无效链接', false);
      }
    };
    return button;
  }

  // ─── 🖥️ 页面插入逻辑 ──────────────────────────────────────────────────────
  // ─── 📊 卡片元数据提取（views / 发布时间 / 频道名 / 频道链接）────────────
  //
  // 覆盖三种 YouTube 卡片 DOM 结构：
  //
  // A. 新版 lockup UI（ytd-rich-item-renderer[lockup]，驼峰式类名）
  //    频道: a[href^="/@"]
  //    views/time: .ytContentMetadataViewModelMetadataRow span.ytContentMetadataViewModelMetadataText
  //
  // B. 旧版 ytd-rich-grid-media（mini-mode）
  //    频道: #channel-name a[href^="/@"]（byline-container 可能 hidden）
  //    views/time: #metadata-line span.inline-metadata-item（第1个=views，第2个=time）
  //
  // C. ytd-grid-video-renderer（频道页/搜索页网格）
  //    标题: a#video-title 的 title 属性
  //    频道: #channel-name a[href^="/@"]（byline-container 可见）
  //    views/time: #metadata-line span（无 inline-metadata-item 类，直接是 span）
  //
  // 策略：每个字段独立尝试所有选择器，只要字段为空就继续尝试下一个
  //
  function extractCardMeta(card) {
    const meta = { views: '', publishedAt: '', channelName: '', channelUrl: '' };

    // ── 频道名 + 频道链接 ──────────────────────────────────────────────────
    const channelLinkSelectors = [
      'a[href^="/@"]',                                    // A. 新版 lockup（实测）
      '.ytLockupMetadataViewModelTextContainer a[href^="/@"]',
      '.ytContentMetadataViewModelMetadataText a[href^="/@"]',
      '#channel-name a[href^="/@"]',                      // B/C. 旧版（ytd-channel-name 内）
      '#channel-name a[href^="/channel"]',
      '#channel-name a[href^="/user"]',
      'ytd-channel-name a[href^="/@"]',
      '#attributed-channel-name a[href]',
      '.ytd-video-meta-block a[href^="/@"]',
    ];
    for (const sel of channelLinkSelectors) {
      const a = card.querySelector(sel);
      if (a) {
        const name = a.textContent.trim();
        const href = a.getAttribute('href') || '';
        if (name && href && href !== 'undefined' &&
            (href.startsWith('/@') || href.startsWith('/channel') || href.startsWith('/user'))) {
          meta.channelName = name;
          meta.channelUrl = a.href || ('https://www.youtube.com' + href);
          break;
        }
      }
    }
    // 频道名兜底：#channel-name yt-formatted-string#text 的 title 属性
    if (!meta.channelName) {
      const cnEl = card.querySelector('#channel-name yt-formatted-string#text, #channel-name #text');
      if (cnEl) {
        const t = cnEl.title || cnEl.textContent.trim();
        if (t && t !== 'undefined') meta.channelName = t;
      }
    }

    // ── 观看数 + 发布时间 ──────────────────────────────────────────────────

    // A. 新版 lockup（驼峰式）：.ytContentMetadataViewModelMetadataRow
    if (!meta.views || !meta.publishedAt) {
      const rows = card.querySelectorAll('.ytContentMetadataViewModelMetadataRow');
      for (const row of rows) {
        const spans = row.querySelectorAll('.ytContentMetadataViewModelMetadataText');
        for (let i = 0; i < spans.length; i++) {
          const text = spans[i].textContent.trim();
          if (/\d.*views?|watching/i.test(text)) {
            if (!meta.views) meta.views = text;
            if (!meta.publishedAt && spans[i + 1]) meta.publishedAt = spans[i + 1].textContent.trim();
            if (!meta.publishedAt && i > 0 && !/views?/i.test(spans[i - 1].textContent))
              meta.publishedAt = spans[i - 1].textContent.trim();
            break;
          }
        }
        if (meta.views) break;
      }
    }

    // B. 旧版 ytd-rich-grid-media：#metadata-line span.inline-metadata-item
    if (!meta.views || !meta.publishedAt) {
      const metaItems = card.querySelectorAll('#metadata-line span.inline-metadata-item');
      for (const span of metaItems) {
        const text = span.textContent.trim();
        if (!meta.views && /\d.*views?|watching/i.test(text)) {
          meta.views = text;
        } else if (!meta.publishedAt && meta.views && text && !/^\s*$/.test(text)) {
          meta.publishedAt = text;
        }
      }
      if (!meta.views && metaItems.length >= 1) meta.views = metaItems[0]?.textContent.trim() || '';
      if (!meta.publishedAt && metaItems.length >= 2) meta.publishedAt = metaItems[1]?.textContent.trim() || '';
    }

    // C. ytd-grid-video-renderer：#metadata-line span（无 inline-metadata-item 类）
    //    过滤掉没有文本内容的节点（dom-repeat 等）
    if (!meta.views || !meta.publishedAt) {
      const allSpans = card.querySelectorAll('#metadata-line span');
      const textSpans = Array.from(allSpans).filter(s => {
        const t = s.textContent.trim();
        return t && t.length > 0 && s.children.length === 0; // 只取叶子节点
      });
      for (const span of textSpans) {
        const text = span.textContent.trim();
        if (!meta.views && /\d.*views?|watching/i.test(text)) {
          meta.views = text;
        } else if (!meta.publishedAt && meta.views && text) {
          meta.publishedAt = text;
        }
      }
      // 如果没有匹配到 views 关键字，按位置取（第1个=views，第2个=time）
      if (!meta.views && textSpans.length >= 1) meta.views = textSpans[0]?.textContent.trim() || '';
      if (!meta.publishedAt && textSpans.length >= 2) meta.publishedAt = textSpans[1]?.textContent.trim() || '';
    }

    // D. 通用兜底：ytd-video-meta-block span.inline-metadata-item
    if (!meta.views || !meta.publishedAt) {
      const metaItems = card.querySelectorAll('ytd-video-meta-block span.inline-metadata-item');
      if (!meta.views && metaItems.length >= 1) meta.views = metaItems[0]?.textContent.trim() || '';
      if (!meta.publishedAt && metaItems.length >= 2) meta.publishedAt = metaItems[1]?.textContent.trim() || '';
    }

    return meta;
  }

  function insertButtonToCard(card) {
    // 按优先级查找视频链接（覆盖新旧版 YouTube DOM）
    const urlSelectors = [
      'a.ytLockupMetadataViewModelTitle',        // 2025+ 驼峰式（实测 DOM）
      'a.yt-lockup-view-model__content-image',   // 连字符式 lockup
      'a.yt-lockup-view-model-wiz__content-image',
      'a#video-title-link',                      // ytd-rich-grid-media
      'a#video-title',                           // ytd-grid-video-renderer
      'a#thumbnail',                             // 旧版
      'a[href*="watch"]',                        // 通用兜底
    ];
    let videoUrl = '';
    for (const sel of urlSelectors) {
      const a = card.querySelector(sel);
      if (a && a.href && a.href.includes('watch')) {
        videoUrl = a.href;
        break;
      }
    }
    if (!videoUrl) return;
    if (card.querySelector('.goqueue-btn')) return;

    // ⚠️ 按钮插入位置：直接 appendChild 到 card，避免插入 lockup 内部容器触发调试器
    const container = document.createElement('div');
    container.style.cssText = 'margin-top:4px;padding:0 4px;';
    container.appendChild(createButton(videoUrl, card));
    card.appendChild(container);
  }

  function insertButtons() {
    const selectors = [
      'ytd-rich-item-renderer:not([data-goqueue-inserted])',
      'ytd-grid-video-renderer:not([data-goqueue-inserted])',
      'ytd-video-renderer:not([data-goqueue-inserted])',
      'ytd-compact-video-renderer:not([data-goqueue-inserted])'
    ];
    const cards = document.querySelectorAll(selectors.join(','));
    cards.forEach(card => {
      card.setAttribute('data-goqueue-inserted', 'true');
      insertButtonToCard(card);
    });
  }

  async function insertButtonToWatchPage() {
    const container = await waitForElement('#top-level-buttons-computed');
    if (!container || container.querySelector('.goqueue-btn')) return;

    const button = createButton(() => location.href, 'watch');
    button.classList.add('goqueue-watch-button');
    container.appendChild(button);
  }

  function waitForElement(selector, timeout = 10000) {
    return new Promise((resolve) => {
      const element = document.querySelector(selector);
      if (element) return resolve(element);
      const observer = new MutationObserver((mutations, obs) => {
        const el = document.querySelector(selector);
        if (el) { obs.disconnect(); resolve(el); }
      });
      observer.observe(document.body, { childList: true, subtree: true });
      setTimeout(() => { observer.disconnect(); resolve(null); }, timeout);
    });
  }

  function startObserver() {
    const grid = document.querySelector('ytd-rich-grid-renderer, #contents');
    if (!grid) return false;
    if (observer) observer.disconnect();
    observer = new MutationObserver(() => insertButtons());
    observer.observe(grid, { childList: true, subtree: true });
    insertButtons();
    return true;
  }

  // ─── 🚀 启动 ──────────────────────────────────────────────────────────────
  let observer = null;
  let lastUrl  = location.href;

  function onUrlChange() {
    if (location.href !== lastUrl) {
      lastUrl = location.href;
      setTimeout(() => {
        if (location.href.includes('watch')) insertButtonToWatchPage();
        else startObserver();
      }, 1000);
    }
  }

  new MutationObserver(onUrlChange).observe(document, { subtree: true, childList: true });

  const wait = setInterval(() => {
    if (location.href.includes('watch')) { insertButtonToWatchPage(); clearInterval(wait); }
    else if (startObserver())            { clearInterval(wait); }
  }, 500);

  window.addEventListener('scroll', insertButtons);

})();
