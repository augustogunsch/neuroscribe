"use strict";

/* The note-types page.
 *
 * A type is a name and a list of extra metadata fields — every note already has
 * a title and a description, and this is how a book record comes to have an
 * author and a year as well. Types are records like any other, sealed with the
 * data key, so the server has no idea any of these fields exist.
 */

/* ---- note types ---- */

const NG_FIELD_TYPES = ["text", "number", "date", "url", "checkbox"];

async function ngViewTypes() {
	const host = ngViewHost();
	const types = Array.from(ngModel.types.values()).sort(ngByName);

	const newType = ngEl("form", { class: "add-chapter", onsubmit: async function (e) {
		e.preventDefault();
		const input = e.target.querySelector("input");
		const name = input.value.trim();
		if (!name) return;
		input.value = "";
		await ngCreateType(name, []);
		ngRender();
	} }, [
		ngEl("input", { name: "name", placeholder: ngT("New type name…"), required: true, maxlength: "200" }),
		ngEl("button", { type: "submit", class: "primary", text: ngT("Create type") }),
	]);

	host.replaceChildren(
		ngEl("nav", { class: "breadcrumb" }, [
			ngEl("a", { href: "/settings", text: ngT("Settings"), dataset: { link: "1" } }),
			ngEl("span", { class: "crumb-sep", text: "/" }),
			ngEl("span", { class: "crumb-here", text: ngT("Note types") }),
		]),
		ngEl("header", { class: "note-header" }, [ngEl("h1", { text: ngT("Note types") })]),
		ngEl("p", { class: "page-hint", text: ngT("Each note has a type. A type defines extra metadata fields — every note always has a title and a description.") }),
		newType,
		ngEl("div", {}, types.map(ngTypeCard)),
	);
	ngSetTitle(ngT("Note types"));
}

function ngTypeCard(type) {
	const used = Array.from(ngModel.notes.values())
		.filter(function (n) { return n.type === type.ref; }).length;
	const fields = type.fields || [];

	const head = ngEl("div", { class: "type-card-head" }, [
		ngEl("h2", { text: type.name }),
		ngEl("span", { class: "type-usage", text: ngTF("%s notes", String(used)) }),
		ngEl("button", { type: "button", text: ngT("Rename"), onclick: async function () {
			const name = await ngAsk(ngT("New type name"), type.name);
			if (!name) return;
			await ngUpdate("type", type.ref, { name: name });
			ngRender();
		} }),
		ngEl("button", { type: "button", class: "danger", text: ngT("Delete type"),
			disabled: used > 0,
			title: used > 0 ? ngTF("cannot delete: %s note(s) use this type", String(used)) : "",
			onclick: async function () {
				if (!(await ngConfirm(ngT("Delete this type?"), true))) return;
				await ngDelete(type.ref);
				ngModel.types.delete(type.ref);
				ngRender();
			} }),
	]);

	const table = fields.length
		? ngEl("table", { class: "fields-table" }, [
			ngEl("thead", {}, [ngEl("tr", {}, [
				ngEl("th", { text: ngT("Field") }),
				ngEl("th", { text: ngT("Data type") }),
				ngEl("th"),
			])]),
			ngEl("tbody", {}, fields.map(function (f, i) {
				return ngEl("tr", {}, [
					ngEl("td", { text: f.label }),
					ngEl("td", {}, [ngEl("code", { text: f.type })]),
					ngEl("td", {}, [
						ngEl("button", { type: "button", class: "danger", title: ngT("Remove field"), text: "✕",
							onclick: async function () {
								const next = fields.slice(0, i).concat(fields.slice(i + 1));
								await ngUpdate("type", type.ref, { fields: next });
								ngRender();
							} }),
					]),
				]);
			})),
		])
		: ngEl("p", { class: "type-empty page-hint", text: ngT("No extra fields.") });

	const label = ngEl("input", { placeholder: ngT("New field label…"), maxlength: "100" });
	const kind = ngEl("select", {}, NG_FIELD_TYPES.map(function (t) {
		return ngEl("option", { value: t, text: t });
	}));
	const addField = ngEl("form", { class: "add-field", onsubmit: async function (e) {
		e.preventDefault();
		const text = label.value.trim();
		if (!text) return;
		const key = text.toLowerCase().replace(/[^a-z0-9]+/g, "_").replace(/^_+|_+$/g, "") || "field";
		await ngUpdate("type", type.ref, {
			fields: fields.concat([{ key: key, label: text, type: kind.value }]),
		});
		ngRender();
	} }, [
		label, kind,
		ngEl("button", { type: "submit", text: ngT("Add field") }),
	]);

	return ngEl("section", { class: "type-card" }, [head, table, addField]);
}
