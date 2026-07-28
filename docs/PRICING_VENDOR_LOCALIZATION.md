# Pricing Vendor Localization

## Objective

Allow `/pricing` to render administrator-managed vendor names in the active
interface language without changing the stable vendor identity used by model
sync, filters, URLs, counts, and colour assignment.

## API Contract

Vendor records expose two distinct fields:

- `name`: required canonical identity. It remains unique and is the only field
  used by vendor matching and upstream synchronization.
- `display_name`: optional localized display content. It may contain one
  adjacent Tokeness `<tnt l="...">...</tnt>` translation group.

`GET /api/pricing` returns both fields in each `vendors[]` item. The frontend
derives `vendor_display_name` for pricing models from the matching vendor.

Display boundaries resolve `display_name` using the active language, English,
then Chinese fallback. An empty `display_name` falls back to the canonical
`name`; invalid `<tnt>` content follows the shared parser contract and remains
raw for administrator correction.

## Compatibility

- Existing databases remain valid because `display_name` is optional.
- SQLite, MySQL, and PostgreSQL receive the field through the existing GORM
  migration path.
- Existing clients can ignore the added response field.
- Vendor synchronization continues to query and create records by `name` only.
- Updating vendor metadata invalidates the pricing cache immediately.

## Acceptance

- English `/pricing` renders `Alibaba` and `Zhipu` when their display names
  contain English translations; Chinese renders `阿里巴巴` and `智谱`.
- Vendor filtering, URL state, model counts, and colour seeds continue to use
  canonical `name` values.
- Empty `display_name` values render the canonical name.
- Updating only `display_name` does not create a duplicate vendor during model
  synchronization.
- Backend tests cover the pricing response and canonical sync identity.
- Frontend tests cover language switching, fallback, and raw filter values.
