// ==UserScript==
// @name        YouTube 投递到 GoQueue 队列
// @namespace   https://github.com/deepseekretro/go-queue-sqlite
// @version     1.0.0
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
  const GOQUEUE_HOST = GM_getValue('goqueue_host', 'http://localhost:8080');

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
  function logVideoInfo(videoTitle, videoUrl) {
    console.group('📋 GoQueue 准备投递');
    console.log('📄 标题:', videoTitle);
    console.log('🔗 链接:', videoUrl);
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
  function enqueueVideo(videoTitle, videoUrl, button) {
    showToast('🚀 投递中...');

    const jobPayload = {
      queue:        QUEUE_NAME,
      job_type:     JOB_TYPE,
      priority:     JOB_PRIORITY,
      max_attempts: MAX_ATTEMPTS,
      tags:         ['youtube'],
      data: {
        title: videoTitle,
        url:   videoUrl,
        // 额外元数据，方便 Worker 侧使用
        queued_at:  new Date().toISOString(),
        source_page: location.href
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
            jobId = resp.id ? ` #${resp.id}` : '';
          } catch (_) {}
          console.log(`✅ 投递成功，Job${jobId}，响应:`, res.responseText);
          showToast(`✅ 已加入队列${jobId}`);
          // 按钮变为「已加入」状态，防止重复投递
          if (button) {
            button.classList.add('goqueue-queued');
            button.disabled = true;
            button.querySelector('span').textContent = '已加入';
          }
        } else {
          console.error('❌ 投递失败，状态码:', res.status, '响应:', res.responseText);
          showToast(`❌ 投递失败: ${res.status}`, false);
        }
      },
      onerror: (err) => {
        console.error('❌ 网络错误:', err);
        showToast('❌ 网络错误，请检查 GoQueue 服务是否运行', false);
      }
    });
  }

  // ─── 🔎 标题获取器 ────────────────────────────────────────────────────────
  function getSafeTitle(context = null, isWatchPage = false) {
    let title = '';
    if (isWatchPage) {
      const h1 = document.querySelector('ytd-watch-metadata h1') || document.querySelector('#title h1');
      if (h1) title = h1.textContent;
      if (!title) title = document.title.replace(' - YouTube', '');
    } else if (context) {
      const newUiHeading = context.querySelector('.yt-lockup-metadata-view-model__heading-reset');
      if (newUiHeading && newUiHeading.title) return newUiHeading.title;
      const newUiLink = context.querySelector('.yt-lockup-metadata-view-model__title');
      if (newUiLink) return newUiLink.textContent.trim();
      const oldTitleEl = context.querySelector('#video-title');
      if (oldTitleEl) title = oldTitleEl.textContent.trim() || oldTitleEl.title || oldTitleEl.getAttribute('aria-label');
      if (!title) {
        const link = context.querySelector('a#video-title-link') || context.querySelector('a#thumbnail');
        if (link) title = link.title || link.getAttribute('aria-label');
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
        logVideoInfo(finalTitle, finalUrl);
        enqueueVideo(finalTitle, finalUrl, button);
      } else {
        showToast('❌ 无效链接', false);
      }
    };
    return button;
  }

  // ─── 🖥️ 页面插入逻辑 ──────────────────────────────────────────────────────
  function insertButtonToCard(card) {
    const aTag = card.querySelector('a.yt-lockup-view-model__content-image')
               || card.querySelector('a#thumbnail')
               || card.querySelector('a');
    const videoUrl = aTag?.href;
    if (!videoUrl || !videoUrl.includes('watch')) return;
    if (card.querySelector('.goqueue-btn')) return;

    const textContainer = card.querySelector('.yt-lockup-metadata-view-model__text-container')
                        || card.querySelector('#meta');

    if (textContainer) {
      textContainer.appendChild(createButton(videoUrl, card));
    } else {
      const container = document.createElement('div');
      container.style.marginTop = '4px';
      container.appendChild(createButton(videoUrl, card));
      card.appendChild(container);
    }
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
