(() => {
  const apiBase = document.body.dataset.apiBase || "";
  const API_BASE = apiBase.replace(/\/$/, "");
  const MODE = (document.body.dataset.mode || "gallery").toLowerCase();
  const LIST_ENDPOINT = MODE === "favorites" ? "/api/favorites" : "/api/posts";
  const analytics = initAnalytics();

  const masonryInstances = {};
  const state = {
    all: { offset: 0, loading: false, done: false },
    h: { offset: 0, loading: false, done: false },
    v: { offset: 0, loading: false, done: false }
  };
  const BATCH_SIZE = 20;
  let activeType = "all";

  const segButtons = Array.prototype.slice.call(document.querySelectorAll('.seg-btn'));
  const segIndicator = document.querySelector('.seg-indicator');

  function readUmamiConfig() {
    const host = (document.body.dataset.umamiHost || '').trim().replace(/\/$/, '');
    const websiteId = (document.body.dataset.umamiWebsiteId || '').trim();
    return { host, websiteId };
  }

  function initAnalytics() {
    const cfg = readUmamiConfig();
    if (!cfg.host || !cfg.websiteId) {
      return { enabled: false };
    }

    if (!window.umami && !document.querySelector('script[data-umami-loader="1"]')) {
      const script = document.createElement('script');
      script.defer = true;
      script.src = cfg.host + '/script.js';
      script.setAttribute('data-website-id', cfg.websiteId);
      script.setAttribute('data-umami-loader', '1');
      document.head.appendChild(script);
    }

    return { enabled: true };
  }

  function trackEvent(name, payload) {
    if (!analytics.enabled) return;
    if (!window.umami || typeof window.umami.track !== 'function') return;
    try {
      if (payload && Object.keys(payload).length > 0) {
        window.umami.track(name, payload);
      } else {
        window.umami.track(name);
      }
    } catch (_) {
      // no-op
    }
  }

  function setActiveButton(type) {
    const idx = segButtons.findIndex(btn => btn.dataset.type === type);
    if (idx === -1) return;
    segButtons.forEach(btn => btn.classList.remove('active'));
    segButtons[idx].classList.add('active');
    if (segIndicator) {
      const target = segButtons[idx];
      segIndicator.style.left = target.offsetLeft + 'px';
      segIndicator.style.width = target.offsetWidth + 'px';
    }
  }

  function setThemeLabel(theme) {
    const btn = document.getElementById('theme-toggle');
    if (!btn) return;
    const sun = '<svg viewBox="0 0 24 24" fill="none" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' +
      '<circle cx="12" cy="12" r="4.2"/>' +
      '<path d="M12 2.5v2.2M12 19.3v2.2M4.5 12H2.3M21.7 12h-2.2M5.6 5.6l1.6 1.6M16.8 16.8l1.6 1.6M18.4 5.6l-1.6 1.6M7.2 16.8l-1.6 1.6"/>' +
      '</svg>';
    const moon = '<svg viewBox="0 0 24 24" fill="none" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' +
      '<path d="M21 12.7A8.5 8.5 0 1 1 11.3 3a6.7 6.7 0 1 0 9.7 9.7z"/>' +
      '</svg>';
    btn.innerHTML = theme === 'dark' ? moon : sun;
  }

  function initTheme() {
    const root = document.documentElement;
    const stored = localStorage.getItem('theme');
    const prefersDark = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
    const theme = stored || (prefersDark ? 'dark' : 'light');
    root.setAttribute('data-theme', theme);
    setThemeLabel(theme);

    const toggle = document.getElementById('theme-toggle');
    if (toggle) {
      toggle.addEventListener('click', function() {
        const next = root.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
        root.setAttribute('data-theme', next);
        localStorage.setItem('theme', next);
        setThemeLabel(next);
      });
    }
  }

  function applyColumns(value, save) {
    const desired = parseInt(value, 10) || 4;
    let applied = desired;
    const width = window.innerWidth || 1200;
    if (width <= 560) {
      applied = Math.min(applied, 1);
    } else if (width <= 860) {
      applied = Math.min(applied, 2);
    }
    document.documentElement.style.setProperty('--cols', applied);
    const display = document.getElementById('cols-value');
    if (display) display.textContent = desired;
    if (save) {
      localStorage.setItem('gallery_cols', String(desired));
    }
    Object.keys(masonryInstances).forEach(k => masonryInstances[k].layout());
  }

  function initColumns() {
    const slider = document.getElementById('cols-range');
    const toggle = document.getElementById('columns-toggle');
    const panel = document.getElementById('cols-panel');
    const control = document.getElementById('cols-control');
    const saved = localStorage.getItem('gallery_cols');

    if (slider) {
      if (saved) slider.value = saved;
      applyColumns(slider.value, false);
      slider.addEventListener('input', function() {
        applyColumns(slider.value, true);
      });
    }

    if (toggle && panel && control) {
      toggle.addEventListener('click', function(e) {
        e.stopPropagation();
        panel.classList.toggle('open');
      });

      document.addEventListener('click', function(e) {
        if (!control.contains(e.target)) {
          panel.classList.remove('open');
        }
      });
    }

    window.addEventListener('resize', function() {
      if (slider) {
        applyColumns(slider.value, false);
      }
    });
  }

  function getGrid(type) {
    return document.getElementById('grid-' + type);
  }

  function initMasonry(type) {
    if (masonryInstances[type]) return;
    const grid = getGrid(type);
    if (!grid) return;
    masonryInstances[type] = new Masonry(grid, {
      itemSelector: '.grid-item',
      columnWidth: '.grid-sizer',
      percentPosition: true,
      gutter: 16
    });
  }

  const observer = lozad('.lozad', {
    rootMargin: '200px 0px',
    threshold: 0,
    loaded: function(el) {
      const revealWhenDecoded = function() {
        if (el.getAttribute('data-loaded') === 'true') {
          return;
        }
        const finalize = function() {
          el.setAttribute('data-loaded', true);
          const item = el.closest('.grid-item');
          if (item) {
            item.classList.add('content-loaded');
            const gridType = item.getAttribute('data-grid');
            if (gridType && masonryInstances[gridType]) {
              masonryInstances[gridType].layout();
            }
          }
        };

        if (typeof el.decode === 'function') {
          el.decode().catch(function() {
            // Some browsers reject decode() for cached or cross-origin images.
          }).finally(finalize);
          return;
        }

        finalize();
      };

      const onImgLoad = function() {
        revealWhenDecoded();
      };

      if (el.complete && el.naturalHeight !== 0) {
        revealWhenDecoded();
      } else {
        el.onload = onImgLoad;
      }
    }
  });

  function svgLink() {
    return '<svg viewBox="0 0 24 24" fill="none" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' +
      '<path d="M10 13a5 5 0 0 1 0-7l1.5-1.5a5 5 0 0 1 7 7L17 12"/>' +
      '<path d="M14 11a5 5 0 0 1 0 7L12.5 19.5a5 5 0 0 1-7-7L7 11"/>' +
      '</svg>';
  }

  function svgDownload() {
    return '<svg viewBox="0 0 24 24" fill="none" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">' +
      '<path d="M12 3v12"/>' +
      '<path d="M7 10l5 5 5-5"/>' +
      '<path d="M5 21h14"/>' +
      '</svg>';
  }

  function buildArtistProfileURL(item) {
    const artistId = String(item.artist_id || '').trim();
    if (!artistId || artistId === 'none') return '';

    const source = String(item.source || '').toLowerCase();
    if (source === 'twitter') {
      return 'https://x.com/' + artistId.replace(/^@+/, '');
    }

    return 'https://www.pixiv.net/users/' + artistId;
  }

  function createItem(item, type, idx) {
    const blankPixel = 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==';
    const pad = (item.height && item.width) ? (item.height / item.width * 100).toFixed(2) : 56.25;
    const previewURL = item.preview_id ? (API_BASE + '/image/' + item.preview_id) : '';
    const originViewURL = item.origin_id ? (API_BASE + '/image/' + item.origin_id) : '';
    const displayURL = previewURL || originViewURL || blankPixel;
    // Use preview in lightbox first to avoid broken modal when origin fetch is unstable.
    const lightboxURL = previewURL || blankPixel;
    const downloadURL = originViewURL ? (originViewURL + '?dl=1') : (previewURL ? (previewURL + '?dl=1') : blankPixel);

    const wrapper = document.createElement('div');
    wrapper.className = 'grid-item';
    wrapper.setAttribute('data-grid', type);

    const link = document.createElement('a');
    link.className = 'lightbox-link';
    link.setAttribute('data-fancybox', 'group-' + type);
    link.setAttribute('data-thumb', displayURL);
    link.setAttribute('href', lightboxURL);
    link.setAttribute('data-caption', (item.title || 'Untitled') + ' - ' + (item.artist_name || ''));
    link.addEventListener('click', function() {
      trackEvent('image_open', {
        mode: MODE,
        segment: type,
        source: item.source || ''
      });
    });

    const ratio = document.createElement('div');
    ratio.className = 'ratio-box';
    ratio.style.paddingTop = pad + '%';

    const img = document.createElement('img');
    img.className = 'lozad';
    img.setAttribute('data-src', displayURL);
    img.setAttribute('alt', (item.title || 'image') + '-' + (idx + 1));
    img.loading = 'lazy';
    img.decoding = 'async';
    if (originViewURL && previewURL && originViewURL !== previewURL) {
      img.addEventListener('error', function() {
        if (img.dataset.fallbackTried === '1') {
          return;
        }
        img.dataset.fallbackTried = '1';
        img.src = originViewURL + (originViewURL.indexOf('?') >= 0 ? '&' : '?') + 'fallback=1';
      });
    }

    ratio.appendChild(img);
    link.appendChild(ratio);
    wrapper.appendChild(link);

    const overlay = document.createElement('div');
    overlay.className = 'card-overlay';

    const meta = document.createElement('div');
    const title = document.createElement('div');
    title.className = 'card-title';
    title.textContent = item.title || 'Untitled';
    meta.appendChild(title);

    if (item.artist_name) {
      const artist = document.createElement('div');
      artist.className = 'card-artist';
      const artistURL = buildArtistProfileURL(item);
      if (artistURL) {
        const link = document.createElement('a');
        link.href = artistURL;
        link.target = '_blank';
        link.rel = 'noopener';
        link.textContent = item.artist_name;
        artist.appendChild(link);
      } else {
        artist.textContent = item.artist_name;
      }
      meta.appendChild(artist);
    }

    const actions = document.createElement('div');
    actions.className = 'card-actions';

    if (item.source_url && item.source_url !== 'none') {
      const origin = document.createElement('a');
      origin.href = item.source_url;
      origin.target = '_blank';
      origin.rel = 'noopener';
      origin.innerHTML = svgLink();
      origin.addEventListener('click', function() {
        trackEvent('source_click', {
          mode: MODE,
          segment: type,
          source: item.source || ''
        });
      });
      actions.appendChild(origin);
    }

    const download = document.createElement('a');
    download.href = downloadURL;
    download.innerHTML = svgDownload();
    download.addEventListener('click', function() {
      trackEvent('download_click', {
        mode: MODE,
        segment: type,
        source: item.source || ''
      });
    });
    actions.appendChild(download);

    overlay.appendChild(meta);
    overlay.appendChild(actions);
    wrapper.appendChild(overlay);

    return wrapper;
  }

  async function fetchBatch(type) {
    if (state[type].loading || state[type].done) return false;
    state[type].loading = true;

    const url = API_BASE + LIST_ENDPOINT + '?type=' + encodeURIComponent(type) + '&offset=' + state[type].offset + '&limit=' + BATCH_SIZE;
    try {
      const res = await fetch(url, { cache: 'no-store' });
      if (!res.ok) {
        state[type].loading = false;
        return false;
      }
      const data = await res.json();
      if (!Array.isArray(data) || data.length === 0) {
        state[type].done = true;
        state[type].loading = false;
        return false;
      }

      const grid = getGrid(type);
      if (!grid) {
        state[type].loading = false;
        return false;
      }

      initMasonry(type);

      const fragment = document.createDocumentFragment();
      const newItems = [];

      data.forEach((item, idx) => {
        const el = createItem(item, type, state[type].offset + idx);
        newItems.push(el);
        fragment.appendChild(el);
      });

      grid.appendChild(fragment);
      state[type].offset += data.length;

      if (masonryInstances[type]) {
        masonryInstances[type].appended(newItems);
        masonryInstances[type].layout();
      }

      observer.observe();
      state[type].loading = false;
      return true;
    } catch (e) {
      state[type].loading = false;
      return false;
    }
  }

  function maybeLoadMore() {
    const nearBottom = (window.innerHeight + window.scrollY) >= (document.body.offsetHeight - 900);
    if (!nearBottom) return;
    fetchBatch(activeType);
  }

  function filterGallery(type, trigger) {
    const prevType = activeType;
    activeType = type;
    setActiveButton(type);

    document.querySelectorAll('.gallery-section').forEach(sec => {
      if (sec.id === 'section-' + type) {
        sec.style.display = 'block';
      } else {
        sec.style.display = 'none';
      }
    });

    fetchBatch(type);

    if (trigger === 'segmented' && prevType !== type) {
      trackEvent('filter_switch', {
        mode: MODE,
        type: type
      });
    }

    setTimeout(() => {
      Object.keys(masonryInstances).forEach(k => masonryInstances[k].layout());
    }, 10);
  }

  document.addEventListener('DOMContentLoaded', function() {
    initTheme();
    initColumns();

    if (window.Fancybox) {
      const mobileViewer = window.matchMedia && window.matchMedia('(max-width: 860px)').matches;
      Fancybox.bind('[data-fancybox]', {
        Thumbs: { autoStart: !mobileViewer },
        Toolbar: {
          display: mobileViewer ? {
            left: ['close'],
            middle: [],
            right: []
          } : {
            left: ['zoom', 'slideshow', 'fullscreen', 'thumbs', 'close'],
            middle: [],
            right: []
          }
        }
      });
    }

    segButtons.forEach(btn => {
      btn.addEventListener('click', function() {
        filterGallery(btn.dataset.type, 'segmented');
      });
    });

    window.addEventListener('resize', function() {
      setActiveButton(activeType);
    });

    setActiveButton(activeType);
    filterGallery(activeType, 'init');
    window.addEventListener('scroll', maybeLoadMore, { passive: true });
  });
})();
