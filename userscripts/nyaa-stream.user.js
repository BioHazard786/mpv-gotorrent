// ==UserScript==
// @name         Nyaa.si - Stream in MPV (mpv-gotorrent)
// @namespace    https://github.com/biohazard786/mpv-gotorrent
// @version      1.0.2
// @description  Adds a 1-click button to stream torrents directly in MPV using mpv-gotorrent
// @author       BioHazard786
// @match        https://nyaa.si/*
// @match        https://*.nyaa.si/*
// @exclude      https://sukebei.nyaa.si/*
// @exclude      https://*.sukebei.nyaa.si/*
// @icon         https://nyaa.si/static/favicon.png
// @grant        none
// @run-at       document-end
// ==/UserScript==

(function () {
    'use strict';

    function addButtons() {
        // 1. Table listings (Search / Browse / User pages)
        const rows = document.querySelectorAll('table.torrent-list tbody tr');
        rows.forEach(row => {
            if (row.querySelector('.mpv-stream-btn')) return;

            const magnet = row.querySelector('a[href^="magnet:"]');
            if (!magnet) return;

            const btn = document.createElement('a');
            btn.className = 'mpv-stream-btn';
            btn.href = 'mpv://' + magnet.href;
            btn.title = 'Play in MPV';
            // Inherit Nyaa's native link styling and FontAwesome icon
            btn.innerHTML = '<i class="fa fa-fw fa-play-circle"></i>';

            btn.addEventListener('click', () => {
                const icon = btn.querySelector('i');
                if (icon) {
                    icon.className = 'fa fa-fw fa-spinner fa-spin';
                    setTimeout(() => { icon.className = 'fa fa-fw fa-play-circle'; }, 3000);
                }
            });

            // Spacing matches native layout between download and magnet links
            magnet.parentNode.appendChild(document.createTextNode(' '));
            magnet.parentNode.appendChild(btn);
        });

        // 2. Torrent Details / View Page (/view/...)
        const viewFooters = document.querySelectorAll('.panel-footer, .card-footer');
        viewFooters.forEach(footer => {
            if (footer.querySelector('.mpv-stream-btn')) return;

            const magnet = footer.querySelector('a[href^="magnet:"]');
            if (!magnet) return;

            const btn = document.createElement('a');
            // Inherit Nyaa's native button classes (e.g. card-footer-item or btn)
            btn.className = (magnet.className ? magnet.className + ' ' : '') + 'mpv-stream-btn';
            btn.href = 'mpv://' + magnet.href;
            btn.title = 'Play in MPV (mpv-gotorrent)';
            btn.innerHTML = '<i class="fa fa-play fa-fw"></i>Play in MPV';

            btn.addEventListener('click', () => {
                const orig = btn.innerHTML;
                btn.innerHTML = '<i class="fa fa-spinner fa-spin fa-fw"></i>Opening MPV...';
                setTimeout(() => { btn.innerHTML = orig; }, 3000);
            });

            // Nyaa details page separates actions with " or "
            const separator = document.createTextNode(' or ');
            magnet.parentNode.insertBefore(separator, magnet.nextSibling);
            magnet.parentNode.insertBefore(btn, separator.nextSibling);
        });
    }

    addButtons();

    // Re-check on dynamic page updates
    const observer = new MutationObserver(() => addButtons());
    observer.observe(document.body, { childList: true, subtree: true });
})();
