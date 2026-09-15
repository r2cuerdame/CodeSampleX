(function() {
  try {
    if (typeof window === 'undefined' || typeof document === 'undefined') return;

    var script = document.getElementById('purple-pulse') || document.currentScript;
    var projectId = (script && script.getAttribute('data-project-id')) || 'pp_codesamplex_f2f2ab10';
    var endpoint = 'https://pulse-api.purpleshiphub.workers.dev/api/v1/ping';
    var version = (script && script.getAttribute('data-version')) || '';
    var env = (script && script.getAttribute('data-env')) || '';

    var isProd = (!env || env === 'production' || env === 'prod');

    if (isProd) {
      var isProdHost = (location.hostname === 'codesamplex.dev' || location.hostname === 'www.codesamplex.dev');
      if (!isProdHost || navigator.webdriver) {
        return;
      }
    } else {
      if (env !== 'test' && env !== 'dev') {
        return;
      }
    }

    function getStorage(key) {
      try {
        if (typeof localStorage !== 'undefined') {
          return localStorage.getItem(key);
        }
      } catch (_) {}
      return null;
    }

    function setStorage(key, val) {
      try {
        if (typeof localStorage !== 'undefined') {
          localStorage.setItem(key, val);
        }
      } catch (_) {}
    }

    function getCookie(name) {
      try {
        var parts = (document.cookie || '').split(';');
        for (var i = 0; i < parts.length; i++) {
          var p = parts[i].trim();
          if (p.indexOf(name + '=') === 0) {
            return decodeURIComponent(p.substring(name.length + 1));
          }
        }
      } catch (_) {}
      return null;
    }

    function setCookie(name, val, days) {
      try {
        var expires = '';
        if (days) {
          var d = new Date();
          d.setTime(d.getTime() + (days * 24 * 60 * 60 * 1000));
          expires = '; expires=' + d.toUTCString();
        }
        document.cookie = name + '=' + encodeURIComponent(val) + expires + '; path=/; SameSite=Lax';
      } catch (_) {}
    }

    function generateUUID() {
      if (typeof crypto !== 'undefined' && crypto.randomUUID) {
        return crypto.randomUUID();
      }
      return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function(c) {
        var r = (Math.random() * 16) | 0;
        var v = c === 'x' ? r : ((r & 0x3) | 0x8);
        return v.toString(16);
      });
    }

    function getInstallId() {
      var key = 'pp_install_id';
      var id = getStorage(key);
      if (!id) {
        id = getCookie(key);
      }
      var uuidRe = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
      if (!id || !uuidRe.test(id)) {
        id = generateUUID();
      }
      setStorage(key, id);
      setCookie(key, id, 365);
      return id;
    }

    function getLocalDay() {
      var d = new Date();
      var m = d.getMonth() + 1;
      var day = d.getDate();
      return d.getFullYear() + '-' + (m < 10 ? '0' : '') + m + '-' + (day < 10 ? '0' : '') + day;
    }

    function hasAttemptedToday(today) {
      var key = 'pp_last_attempt';
      var last = getStorage(key);
      if (!last) {
        last = getCookie(key);
      }
      return last === today;
    }

    function markAttemptedToday(today) {
      var key = 'pp_last_attempt';
      setStorage(key, today);
      setCookie(key, today, 7);
    }

    var today = getLocalDay();
    if (hasAttemptedToday(today)) {
      return;
    }

    function getOS() {
      try {
        if (navigator.userAgentData && navigator.userAgentData.platform) {
          var p = navigator.userAgentData.platform.toLowerCase();
          if (p.indexOf('win') !== -1) return 'windows';
          if (p.indexOf('android') !== -1) return 'android';
          if (p.indexOf('ios') !== -1 || p.indexOf('iphone') !== -1 || p.indexOf('ipad') !== -1) return 'ios';
          if (p.indexOf('mac') !== -1) return 'macos';
          if (p.indexOf('linux') !== -1) return 'linux';
        }
        var ua = (navigator.userAgent || '').toLowerCase();
        var plat = (navigator.platform || '').toLowerCase();
        if (ua.indexOf('win') !== -1 || plat.indexOf('win') !== -1) return 'windows';
        if (ua.indexOf('android') !== -1) return 'android';
        if (ua.indexOf('iphone') !== -1 || ua.indexOf('ipad') !== -1 || ua.indexOf('ipod') !== -1) return 'ios';
        if (ua.indexOf('mac') !== -1 || plat.indexOf('mac') !== -1) return 'macos';
        if (ua.indexOf('linux') !== -1 || plat.indexOf('linux') !== -1) return 'linux';
      } catch (_) {}
      return 'other';
    }

    var events = ['pointerdown', 'touchstart', 'keydown'];
    var hasFired = false;

    function handleInteraction(event) {
      if (isProd && (!event || event.isTrusted !== true)) return;
      if (hasFired) return;
      hasFired = true;

      for (var i = 0; i < events.length; i++) {
        document.removeEventListener(events[i], handleInteraction);
      }

      if (hasAttemptedToday(today)) {
        return;
      }

      var installId = getInstallId();
      var os = getOS();

      var payload = {
        project_id: projectId,
        install_id: installId,
        version: version,
        os: os,
        platform: 'web',
        schema_version: 2
      };

      if (!isProd) {
        payload.environment = env;
      }

      // Persist attempt day immediately before fetch to prevent retry storms across page loads.
      markAttemptedToday(today);

      if (typeof fetch !== 'function') return;

      var signal;
      var timer;
      if (typeof AbortSignal !== 'undefined' && AbortSignal.timeout) {
        signal = AbortSignal.timeout(2000);
      } else if (typeof AbortController !== 'undefined') {
        var controller = new AbortController();
        timer = setTimeout(function() { controller.abort(); }, 2000);
        signal = controller.signal;
      }

      fetch(endpoint, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify(payload),
        mode: 'cors',
        credentials: 'omit',
        keepalive: true,
        signal: signal
      }).then(function() {
        if (timer) clearTimeout(timer);
      }).catch(function() {
        if (timer) clearTimeout(timer);
      });
    }

    for (var i = 0; i < events.length; i++) {
      document.addEventListener(events[i], handleInteraction, { once: true, passive: true });
    }

  } catch (_) {
    // Fail silently.
  }
})();
