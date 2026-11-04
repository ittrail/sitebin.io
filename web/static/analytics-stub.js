// Sitebin analytics is not in effect on this address: umami.track and
// umami.identify send nothing and resolve at once, as the real ones would,
// so a page that calls them (or waits on them) never throws.
window.umami = window.umami || { track() { return Promise.resolve(); }, identify() { return Promise.resolve(); } };
