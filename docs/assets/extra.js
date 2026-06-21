/* taboo landing — progressive enhancement for "the door" (the .tbl page).
   Pure enhancement: the landing is fully readable if this file is blocked.
   Re-runs on every Material instant-navigation load via document$. */
(function () {
  "use strict";

  var reduceMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;

  function initLanding() {
    var root = document.querySelector(".tbl");
    if (!root || root.dataset.tblInit) return;   // only on the landing, once per DOM
    root.dataset.tblInit = "1";

    /* ---- Entrance reveals ------------------------------------------- */
    var reveals = Array.prototype.slice.call(root.querySelectorAll("[data-reveal]"));
    var revealAll = function () { reveals.forEach(function (el) { el.classList.add("in"); }); };

    if (reduceMotion || !("IntersectionObserver" in window)) {
      revealAll();
    } else {
      root.classList.add("tbl-reveal-ready");      // arm the pre-reveal hidden state
      var io = new IntersectionObserver(function (entries, obs) {
        entries.forEach(function (entry) {
          if (entry.isIntersecting) { entry.target.classList.add("in"); obs.unobserve(entry.target); }
        });
      }, { rootMargin: "0px 0px -8% 0px", threshold: 0.05 });
      reveals.forEach(function (el) { io.observe(el); });
      // failsafe: nothing stays hidden even if the observer never fires
      setTimeout(revealAll, 1800);
    }

    /* ---- Copy-to-clipboard chips ------------------------------------ */
    var copyText = function (text) {
      if (navigator.clipboard && window.isSecureContext) {
        return navigator.clipboard.writeText(text).then(function () { return true; }, function () { return false; });
      }
      try {
        var ta = document.createElement("textarea");
        ta.value = text; ta.setAttribute("readonly", "");
        ta.style.position = "absolute"; ta.style.left = "-9999px";
        document.body.appendChild(ta); ta.select();
        var ok = document.execCommand("copy");
        document.body.removeChild(ta);
        return Promise.resolve(ok);
      } catch (e) { return Promise.resolve(false); }
    };

    root.querySelectorAll(".copy").forEach(function (chip) {
      var btn = chip.querySelector(".copy__btn");
      var label = chip.querySelector(".copy__label");
      if (!btn || !label) return;
      var original = label.textContent;
      var originalAria = btn.getAttribute("aria-label") || "Copy";
      var timer;
      btn.addEventListener("click", function () {
        copyText(chip.getAttribute("data-copy") || "").then(function (ok) {
          label.textContent = ok ? "Copied" : "Press ⌘C";
          chip.classList.toggle("is-copied", ok);
          btn.setAttribute("aria-label", ok ? "Copied to clipboard" : "Copy failed");
          clearTimeout(timer);
          timer = setTimeout(function () {
            label.textContent = original;
            chip.classList.remove("is-copied");
            btn.setAttribute("aria-label", originalAria);
          }, 2000);
        });
      });
    });
  }

  /* ---- Run now, and again after each Material instant navigation ---- */
  if (typeof window.document$ !== "undefined" && window.document$.subscribe) {
    window.document$.subscribe(initLanding);
  } else if (document.readyState !== "loading") {
    initLanding();
  } else {
    document.addEventListener("DOMContentLoaded", initLanding);
  }
})();
