"use strict";

/* The interface's own words.
 *
 * Pages are built in the browser, so there is no template pass to translate
 * in: a language is fetched once as a table and kept in localStorage — which
 * is also what makes the interface speak Portuguese on a device with no
 * connection.
 *
 * A missing key falls back to the English text, which is also the key, so a
 * translation that has not caught up yet reads as English rather than blank.
 */

let NG_STRINGS = {};

function ngSetStrings(table) {
	NG_STRINGS = table || {};
}

function ngT(text) {
	return NG_STRINGS[text] || text;
}
