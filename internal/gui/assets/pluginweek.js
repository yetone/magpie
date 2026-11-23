// the day in the reader's own calendar, which the daily pick turns on
function today() {
  const date = new Date();
  return Math.floor((date.getTime() - date.getTimezoneOffset() * 6e4) / 864e5);
}

export function picks(listings, { usersOf, likesOf, hotOf, byHot, entryOf }) {
  const used = listings.filter((item) => usersOf(item) > 0).sort((left, right) =>
    usersOf(right) - usersOf(left) || likesOf(right) - likesOf(left) || byHot(left, right));
  const liked = listings.filter((item) => likesOf(item) > 0).sort((left, right) =>
    likesOf(right) - likesOf(left) || usersOf(right) - usersOf(left) || byHot(left, right));
  // with no counts from magpie (offline, or none yet), npm's downloads
  const downloaded = used.length ? [] : listings.filter((item) => !item.github && hotOf(item) > 0).sort(byHot);
  // the day's: one of the community's most used not installed yet, a
  // different one each day
  const eligible = (used.length ? used : downloaded).filter((item) => !item.github);
  if (!eligible.length) return null;
  let pool = eligible.filter((item) => item.community && !entryOf(item.package)).slice(0, 14);
  if (!pool.length) pool = eligible.slice(0, 14);
  return { day: pool[today() % pool.length], used: used.slice(0, 5), liked: liked.slice(0, 5), downloaded: downloaded.slice(0, 5) };
}
