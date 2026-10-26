// ==UserScript==
// @name         1337x - Stream in MPV (mpv-gotorrent)
// @namespace    https://github.com/biohazard786/mpv-gotorrent
// @version      1.0.2
// @description  Adds a 1-click button to stream torrents directly in MPV using mpv-gotorrent
// @author       BioHazard786
// @match        https://1337x.to/*
// @match        https://*.1337x.to/*
// @match        https://1337x.st/*
// @match        https://*.1337x.st/*
// @match        https://1337x.ws/*
// @match        https://*.1337x.ws/*
// @match        https://1337x.eu/*
// @match        https://*.1337x.eu/*
// @match        https://1337x.se/*
// @match        https://*.1337x.se/*
// @match        https://1337x.is/*
// @match        https://*.1337x.is/*
// @match        https://1337x.gd/*
// @match        https://*.1337x.gd/*
// @match        https://x1337x.ws/*
// @match        https://*.x1337x.ws/*
// @match        https://x1337x.eu/*
// @match        https://*.x1337x.eu/*
// @match        https://x1337x.se/*
// @match        https://*.x1337x.se/*
// @icon         https://1337x.to/favicon.ico
// @grant        none
// @run-at       document-end
// ==/UserScript==

(function () {
    'use strict';

    function addButtons() {
        // 1. Torrent Details Page (/torrent/...)
        const detailMagnet = document.querySelector('a[href^="magnet:"]');
        if (detailMagnet && !document.querySelector('.mpv-detail-stream-btn')) {
            // Clone the magnet anchor to inherit 1337x's exact obfuscated button classes & styles
            const btn = detailMagnet.cloneNode(false);
            btn.classList.add('mpv-detail-stream-btn');
            btn.removeAttribute('onclick');
            btn.href = 'mpv://' + detailMagnet.href;
            btn.title = 'Play in MPV (mpv-gotorrent)';

            // Replicate the native <span class="icon"> box with a crisp white play icon
            btn.innerHTML = '<span class="icon"><svg viewBox="0 0 24 24" width="16" height="16" fill="#ffffff" style="vertical-align: middle; display: inline-block; margin-top: -2px;"><polygon points="7,4 19,12 7,20"/></svg></span>Play in MPV';

            const parentLi = detailMagnet.closest('li');
            if (parentLi && parentLi.parentNode) {
                const li = parentLi.cloneNode(false);
                li.className = (parentLi.className ? parentLi.className + ' ' : '') + 'mpv-stream-li';
                li.appendChild(btn);
                parentLi.parentNode.insertBefore(li, parentLi.nextSibling);
            } else if (detailMagnet.parentNode) {
                btn.style.marginLeft = '10px';
                detailMagnet.parentNode.insertBefore(btn, detailMagnet.nextSibling);
            }
        }

        // 2. Listing & Search Pages (/search/..., /popular, /top-100, /cat/...)
        const nameCells = document.querySelectorAll('table.table-list tbody tr td.name');
        nameCells.forEach(td => {
            if (td.querySelector('.mpv-list-stream-btn')) return;

            const torrentLink = td.querySelector('a[href^="/torrent/"]');
            if (!torrentLink) return;

            const playBtn = document.createElement('a');
            playBtn.className = 'mpv-list-stream-btn';
            playBtn.href = 'javascript:void(0);';
            playBtn.title = 'Play in MPV';
            // Use 1337x's official download green (#89ad19)
            playBtn.style.cssText = `
                display: inline-block;
                color: #89ad19;
                font-size: 12px;
                margin-right: 6px;
                cursor: pointer;
                text-decoration: none;
                transition: transform 0.15s ease, color 0.15s ease;
            `;
            playBtn.innerHTML = '▶';

            playBtn.addEventListener('mouseenter', () => {
                playBtn.style.color = '#a0cb1e';
                playBtn.style.transform = 'scale(1.25)';
            });
            playBtn.addEventListener('mouseleave', () => {
                playBtn.style.color = '#89ad19';
                playBtn.style.transform = 'scale(1)';
            });

            playBtn.addEventListener('click', async (e) => {
                e.preventDefault();
                e.stopPropagation();

                // If already fetched on this row, launch directly
                if (playBtn.dataset.magnet) {
                    window.location.href = 'mpv://' + playBtn.dataset.magnet;
                    return;
                }

                playBtn.innerHTML = '⏳';
                playBtn.style.cursor = 'wait';

                try {
                    const resp = await fetch(torrentLink.href);
                    if (!resp.ok) throw new Error('HTTP ' + resp.status);
                    const html = await resp.text();
                    const parser = new DOMParser();
                    const doc = parser.parseFromString(html, 'text/html');
                    const magnet = doc.querySelector('a[href^="magnet:"]')?.href;

                    if (magnet) {
                        playBtn.dataset.magnet = magnet;
                        playBtn.innerHTML = '✔';
                        playBtn.title = 'Play in MPV';
                        window.location.href = 'mpv://' + magnet;
                        setTimeout(() => { playBtn.innerHTML = '▶'; }, 1500);
                    } else {
                        throw new Error('Magnet not found');
                    }
                } catch (err) {
                    console.error('mpv-gotorrent failed to load magnet:', err);
                    playBtn.innerHTML = '❌';
                    setTimeout(() => { playBtn.innerHTML = '▶'; }, 2000);
                } finally {
                    playBtn.style.cursor = 'pointer';
                }
            });

            td.insertBefore(playBtn, torrentLink);
        });
    }

    addButtons();

    // Re-check on dynamic page updates
    const observer = new MutationObserver(() => addButtons());
    observer.observe(document.body, { childList: true, subtree: true });
})();
