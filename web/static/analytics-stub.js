// Sitebin analytics is not in effect on this address: umami.track and
// umami.identify do nothing, so a page that calls them never throws.
window.umami = window.umami || {track(){}, identify(){}};
