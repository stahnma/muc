// Keeps the sidebar open where the page fits beside it, and leaves it
// tucked into the hover rail (see styles.css) where it would not.
(function() {
    const PINNED_CLASS = 'sidebar-pinned';
    const MEASURING_CLASS = 'sidebar-measuring';

    // Wide tables that scroll inside their own box rather than the page.
    // Code blocks and command output are left out: a long line scrolls
    // there at any width, so it says nothing about the sidebar.
    const SCROLL_BOXES = '.hw-table-wrap';

    // The rail only exists where there is a mouse and room for a sidebar;
    // elsewhere the class would do nothing, so leave it off.
    const railQuery = window.matchMedia('(min-width: 901px) and (hover: hover)');

    function overflows() {
        const root = document.documentElement;
        if (root.scrollWidth > root.clientWidth) {
            return true;
        }
        for (const box of document.querySelectorAll(SCROLL_BOXES)) {
            if (box.scrollWidth > box.clientWidth) {
                return true;
            }
        }
        return false;
    }

    // Always try pinned first and fall back, all before the next paint, so
    // the answer depends only on the window and the content, not on which
    // way it was last time, and it cannot flip back and forth. Transitions
    // are held off throughout, or the trial would start the rail sliding.
    function update() {
        const root = document.documentElement;
        if (!railQuery.matches) {
            root.classList.remove(PINNED_CLASS);
            return;
        }
        root.classList.add(MEASURING_CLASS, PINNED_CLASS);
        if (overflows()) {
            root.classList.remove(PINNED_CLASS);
        }
        // Settle the styles while transitions are still off.
        const sidebar = document.querySelector('.sidebar');
        if (sidebar) {
            getComputedStyle(sidebar).transform;
        }
        root.classList.remove(MEASURING_CLASS);
    }

    let queued = false;
    function schedule() {
        if (queued) {
            return;
        }
        queued = true;
        requestAnimationFrame(() => {
            queued = false;
            update();
        });
    }

    function init() {
        update();
        window.addEventListener('resize', schedule);
        railQuery.addEventListener('change', schedule);
        // The dashboard and the other pages fill in their tables after
        // load and as hosts report, which can change how wide they are.
        const page = document.querySelector('.page');
        if (page) {
            new MutationObserver(schedule).observe(page, { childList: true, subtree: true });
        }
        // Web fonts change text widths once they arrive.
        if (document.fonts) {
            document.fonts.ready.then(schedule);
        }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();
