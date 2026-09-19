// Index choreography and scroll reveals, extracted for a strict script CSP.
(function () {
  var toggle = document.querySelector('.nav-toggle');
  var menu = document.getElementById('site-menu');
  var root = document.documentElement;
  var closeTimer = null;
  var background = document.querySelectorAll('header, main, footer');

  function isOpen() {
    return toggle.getAttribute('aria-expanded') === 'true';
  }

  function openMenu() {
    window.clearTimeout(closeTimer);
    menu.hidden = false;
    root.classList.add('menu-open');
    toggle.setAttribute('aria-expanded', 'true');
    background.forEach(function (element) {
      element.inert = true;
    });
    // Next frame, so the transition has a from-state to animate out of.
    window.requestAnimationFrame(function () {
      menu.classList.add('is-open');
    });
  }

  function closeMenu() {
    menu.classList.remove('is-open');
    root.classList.remove('menu-open');
    toggle.setAttribute('aria-expanded', 'false');
    background.forEach(function (element) {
      element.inert = false;
    });
    // Let the fade play before the panel is removed from the a11y tree.
    closeTimer = window.setTimeout(function () {
      menu.hidden = true;
    }, 480);
  }

  toggle.addEventListener('click', function () {
    if (isOpen()) closeMenu();
    else openMenu();
  });

  document.addEventListener('keydown', function (event) {
    if (event.key === 'Escape' && isOpen()) {
      closeMenu();
      toggle.focus();
    }
    if (event.key === 'Tab' && isOpen()) {
      var focusable = [toggle].concat(Array.from(menu.querySelectorAll('a')));
      var first = focusable[0];
      var last = focusable[focusable.length - 1];
      if (
        event.shiftKey &&
        (document.activeElement === first ||
          !focusable.includes(document.activeElement))
      ) {
        event.preventDefault();
        last.focus();
      } else if (
        !event.shiftKey &&
        (document.activeElement === last ||
          !focusable.includes(document.activeElement))
      ) {
        event.preventDefault();
        first.focus();
      }
    }
  });

  menu.addEventListener('click', function (event) {
    if (event.target.closest('a')) closeMenu();
  });

  var targets = document.querySelectorAll('[data-reveal]');
  var reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  if (reduced || !('IntersectionObserver' in window)) {
    Array.prototype.forEach.call(targets, function (el) {
      el.classList.add('is-visible');
    });
  } else {
    var observer = new IntersectionObserver(
      function (entries) {
        entries.forEach(function (entry) {
          if (!entry.isIntersecting) return;
          entry.target.classList.add('is-visible');
          observer.unobserve(entry.target);
        });
      },
      { threshold: 0.12, rootMargin: '0px 0px -8% 0px' },
    );

    Array.prototype.forEach.call(targets, function (el) {
      observer.observe(el);
    });
  }
})();
