# Subscription Promo Code Design

Date: 2026-08-26
Status: Approved for implementation on 2026-08-27

## 1. Objective

Add single-use percentage promo codes for subscription purchases. A valid code
reduces the price of any subscription plan, can be used for either a new
subscription or a renewal, and becomes permanently unavailable after one
successful payment.

The implementation must preserve the existing registration promo-code behavior,
keep all pricing decisions on the server, and remain consistent with payment
order cancellation, expiry, recovery, refund, and affiliate-rebate behavior.

## 2. Confirmed Requirements

### Included

- Add a subscription-only promo-code purpose alongside the existing
  registration-bonus purpose.
- Support percentage discounts only, such as:
  - 10% off / nine-tenths price: customer pays 90%.
  - 20% off / eight-tenths price: customer pays 80%.
- Apply a subscription promo code to every subscription plan without plan or
  group restrictions.
- Allow use for both first-time purchases and renewals.
- Allow at most one promo code on an order.
- Make every subscription promo code globally single-use after successful
  payment.
- Reserve the code while its payment order is pending.
- Release the code when the unpaid order reaches its final cancelled or expired
  state, allowing the same code to be tried again.
- Keep the reservation through the existing five-minute late-payment recovery
  window before final release.
- Reject an order whose final payable amount rounds to zero.
- Calculate affiliate rebate from the discounted subscription amount.
- Display the original price, discount, and discounted price before payment.
- Record enough immutable order data to explain historical pricing even if the
  promo code or subscription plan is later edited or deleted.

### Excluded

- Fixed-amount discounts.
- Promo-code stacking.
- Free or zero-value subscription orders.
- Plan-, group-, platform-, first-purchase-, or renewal-specific restrictions.
- Minimum-spend rules.
- Per-user campaigns or reusable subscription promo codes.
- Automatic coupon assignment or a user coupon wallet.
- Applying subscription promo codes to balance recharge orders.
- Retroactive discounts on existing orders.

## 3. Terminology and Amount Semantics

The design uses the following terms:

- `original_amount`: the subscription plan price captured when the order is
  created.
- `discount_rate`: the multiplier applied to the original price. `0.9000` means
  the customer pays 90%, while `0.8000` means the customer pays 80%.
- `discount_amount`: `original_amount - discounted_amount`, rounded using the
  plan-price precision.
- `amount`: the discounted subscription amount before payment-channel currency
  conversion and payment fees.
- `pay_amount`: the amount expected by the selected payment provider after any
  configured subscription currency conversion and payment fee.

Affiliate rebate uses `amount`, not `pay_amount`. This makes rebate follow the
actual discounted subscription value without accidentally including a payment
fee or treating a CNY-converted gateway amount as a USD rebate base.

Example for a USD 100 plan with an eight-tenths-price code:

```text
original_amount = 100.00
discount_rate   = 0.8000
discount_amount = 20.00
amount          = 80.00
pay_amount      = currency conversion and fee applied to 80.00
affiliate base  = 80.00
```

All arithmetic must use the repository's existing decimal/currency helpers. The
backend is authoritative; frontend calculations are previews only.

## 4. Data Model

### 4.1 Promo codes

Extend `promo_codes` without changing existing rows' behavior:

- `purpose`: `registration_bonus` or `subscription_discount`; existing rows
  migrate to `registration_bonus`.
- `discount_rate`: nullable decimal multiplier. It is required for
  `subscription_discount` and absent for `registration_bonus`.

Subscription promo-code validation rules:

- `purpose` is `subscription_discount`.
- status is active.
- expiry has not passed.
- `discount_rate` is greater than zero and less than one.
- the code has no consumed use.
- the code has no active reservation owned by another live order.
- its effective maximum use count is exactly one.

Existing fields such as code, status, expiry, notes, and generated-code behavior
remain shared. `bonus_amount` remains meaningful only for registration promo
codes. The admin UI fixes the maximum uses of subscription promo codes to one
rather than exposing a misleading configurable value.

### 4.2 Promo-code usage and reservation

Extend `promo_code_usages` to represent one order attempt:

- nullable `payment_order_id` for backward compatibility with registration
  usages;
- `usage_type`: `registration_bonus` or `subscription_discount`;
- `status`: `reserved`, `consumed`, or `released`;
- `discount_amount`, default zero;
- nullable `reserved_at`, `consumed_at`, and `released_at` timestamps.

Existing registration usage rows migrate to `usage_type=registration_bonus` and
`status=consumed`.

Replace the current `(promo_code_id, user_id)` uniqueness rule with order-based
identity for subscription attempts. Released attempts must remain as audit
history, including when the same user retries the same code with a new order.
Only one successful consumption is allowed globally for a subscription promo
code. Reservation and consumption occur while holding the promo-code row lock,
and database constraints protect order-level idempotency. Preserve the existing
one-use-per-user rule for registration promo codes with a partial unique
constraint on `(promo_code_id, user_id)` for `registration_bonus` rows. Add
partial unique constraints that allow at most one `reserved` row and at most one
`consumed` row per subscription promo code, plus a unique non-null
`payment_order_id`.

### 4.3 Payment orders

Add immutable discount snapshot fields to `payment_orders`:

- nullable `promo_code_id`;
- nullable `promo_code` text snapshot;
- nullable `original_amount`;
- nullable `discount_rate`;
- `discount_amount`, default zero.

For discounted subscription orders, existing `amount` stores the discounted
subscription amount and existing `pay_amount` stores the expected gateway
amount. Historical orders and orders without promo codes keep their current
meaning and behavior.

The order snapshot, rather than the mutable promo-code row, is the authority for
display, payment reconciliation, refund calculations, and affiliate rebate.

## 5. Promo-Code State Machine

```text
available
  -> reserved  when a pending subscription order is created
  -> consumed  when that order is successfully paid

reserved
  -> released  after the unpaid order is finally cancelled/expired
  -> consumed  if payment succeeds during the recovery window

released
  -> reserved  by a later order

consumed
  -> terminal; the code can never be reserved or consumed again
```

The existing configured order timeout remains the reservation timeout source.
Its default is 30 minutes. No separate coupon timer is introduced.

When an order reaches timeout, the existing lifecycle first reconciles with the
payment provider. If payment has already succeeded, the code is consumed and the
subscription is fulfilled. Otherwise the order becomes expired, but the
reservation remains for the existing five-minute payment recovery grace period.
After that grace period, the reservation becomes released.

Manual cancellation retains the reservation through the same five-minute safety
window before release. A payment-creation failure releases immediately when no
provider order was created. Release is idempotent: repeated cancellation,
expiry sweeps, or provider errors cannot release a consumed code or create
duplicate history.

A payment notification received after the reservation has been finally released
must not silently grant a second discounted subscription if another order has
since reserved or consumed the code. It is recorded as a late-payment exception
for reconciliation/refund handling using the existing payment audit mechanism.

## 6. Backend Flow

### 6.1 Preview validation

Add an authenticated payment endpoint that accepts:

```json
{
  "code": "SAVE20",
  "plan_id": 123
}
```

It returns the normalized code, discount rate, original amount, discount amount,
discounted amount, and validity. It does not reserve or consume the code.

The endpoint is separate from the public registration promo-code validator
because the two purposes return different values and subscription validation
requires a real plan and authenticated purchaser.

Preview success does not guarantee later use. `CreateOrder` always repeats the
validation under a transaction and promo-code row lock.

### 6.2 Order creation and reservation

Extend the subscription order request with one optional `promo_code` string.
Balance order requests containing a subscription promo code are rejected.

Inside order creation:

1. Load and validate the plan on the server.
2. Normalize and lock the promo-code row.
3. Validate purpose, status, expiry, discount rate, consumption, and reservation.
4. Calculate the discounted subscription amount with decimal arithmetic.
5. Reject a non-positive or currency-invalid final amount.
6. Create the payment order with the complete discount snapshot.
7. Create the `reserved` usage record linked to that order.
8. Commit the order and reservation together.
9. Calculate currency conversion and payment fee from the discounted amount.
10. Invoke the selected payment provider with the resulting `pay_amount`.

If provider order creation fails, mark the payment order failed and release the
reservation idempotently.

### 6.3 Payment success

The existing callback amount check continues to compare the provider-reported
amount with the snapshotted `pay_amount`.

Before completing subscription fulfillment, atomically transition the linked
promo usage from `reserved` to `consumed` and increment the promo code's consumed
count. Retried or duplicate callbacks see the existing consumed record and
continue idempotently without consuming twice.

Subscription assignment remains protected by the existing fulfillment
idempotency mechanism.

### 6.4 Cancellation, expiry, and late payment

Cancellation and expiry call one shared idempotent release operation. It only
transitions `reserved` to `released`; it never changes `consumed`.

Automatic expiry and manual cancellation retain the reservation through the
current five-minute late-payment recovery window. Extend the existing payment
order expiry service with a release sweep for cancelled or expired discounted
orders whose recovery window has ended; no new background service is needed.

Late payment before release consumes the reservation and fulfills normally.
Late payment after final release is audited and does not automatically fulfill a
discounted subscription.

### 6.5 Refunds

Refund calculations use the discounted order `amount` and gateway `pay_amount`,
so a full refund cannot exceed what was charged. A refund does not restore the
promo code: successful payment permanently consumes it, matching the confirmed
single-success rule.

### 6.6 Affiliate rebate

The existing affiliate rebate path uses the payment order's `amount`. Storing the
discounted subscription value in `amount` therefore makes the rebate base correct
without introducing a second rebate calculation path. Audit details continue to
record the chosen base amount.

## 7. WeChat Payment Recovery

The WeChat in-app flow performs OAuth before recreating the payment request. Add
the normalized promo code to:

- the OAuth start URL context;
- the server-side OAuth cookie context;
- the signed `WeChatPaymentResumeClaims` payload;
- request-claim consistency checks;
- the frontend resume-route parser and cleanup.

The resumed `CreateOrder` call still performs current server-side promo-code
validation and reservation. The signed context prevents browser tampering but is
not treated as proof that the code remains available.

## 8. Frontend Design

### User subscription checkout

On the selected-plan confirmation screen:

- show one optional promo-code input;
- provide an explicit Apply action;
- show validation errors without creating an order;
- after success, display original price, discount rate, discount amount, and
  discounted price;
- calculate the displayed payment fee from the discounted amount;
- submit the normalized code with the final create-order request;
- clear the applied preview if the user changes plans, even though all plans are
  eligible, so the server preview and displayed price cannot drift;
- keep the code through supported WeChat payment recovery.

The frontend must use the amounts returned by the preview endpoint. It must not
send a client-calculated discounted amount as an authority.

### Admin promo-code management

Extend the existing promo-code screen rather than adding a second management
area:

- select purpose: registration bonus or subscription discount;
- for registration bonus, retain bonus amount and maximum-use controls;
- for subscription discount, show a payable-percentage input such as 90% or 80%;
- persist it as `discount_rate` (`0.9` or `0.8`);
- fix maximum successful uses to one;
- list purpose, discount, reservation/usage state, order, user, and timestamps.

Existing registration promo codes remain editable and behave as before.

## 9. API and Error Contract

Use stable error reasons for at least:

- code not found;
- wrong promo-code purpose;
- disabled or expired code;
- invalid discount rate;
- code already consumed;
- code currently reserved;
- invalid or unavailable subscription plan;
- promo code supplied to a non-subscription order;
- discounted amount not payable;
- reservation conflict caused by concurrent orders.

Error messages must not reveal another purchaser's identity or order details.
Concurrent attempts for the same code have one winner; other attempts receive a
deterministic unavailable/reserved response.

## 10. Security and Consistency Rules

- Never trust a frontend price, discount amount, or discount rate.
- Normalize codes consistently before lookup and snapshotting.
- Lock the promo-code row while reserving or consuming.
- Commit order creation and reservation in one transaction.
- Store immutable pricing evidence on the order.
- Preserve existing provider signature, amount, currency, and idempotency checks.
- Do not log full payment payloads or secrets.
- Do not restore a consumed promo code after refund.
- Keep existing registration promo-code behavior backward compatible.

## 11. Testing Strategy

### Promo-code model and service

- Existing registration codes migrate and continue granting balance.
- Subscription codes accept valid rates such as `0.9` and `0.8`.
- Rates less than or equal to zero and greater than or equal to one are rejected.
- Disabled, expired, reserved, and consumed codes return distinct results.
- Released codes can be reserved again.
- Consumed codes cannot be released or reused.

### Order pricing

- Nine-tenths and eight-tenths price calculations use decimal rounding.
- All subscription plans and renewals accept the code.
- Balance recharge rejects the code.
- One request cannot carry more than one code.
- Final zero/minor-unit-invalid amounts are rejected.
- Fee and subscription currency conversion start from the discounted amount.
- Order snapshots preserve the original amount, rate, discount, code, and final
  amount.

### Concurrency and lifecycle

- Concurrent orders for one code produce exactly one reservation.
- Duplicate create-order retries do not create duplicate reservations.
- Provider creation failure releases the reservation.
- Manual cancellation releases safely.
- Automatic timeout checks upstream payment before expiry.
- Payment during the five-minute recovery window consumes the reservation.
- Final expiry releases it after the recovery window.
- Duplicate success callbacks consume and fulfill exactly once.
- A callback after final release is audited and does not double-fulfill.

### Refund and affiliate rebate

- Full refund is based on the discounted amount actually charged.
- Refund does not restore the promo code.
- Affiliate rebate uses discounted `amount`, excluding fee and currency
  conversion.
- Affiliate rebate remains idempotent under repeated fulfillment.

### Frontend

- Applying a valid code updates the price breakdown.
- Invalid/reserved/consumed responses display actionable messages.
- Changing plans clears stale preview state.
- Create-order payload contains the applied code and no client-authoritative
  discount fields.
- WeChat OAuth and resume retain the code.
- Admin registration and subscription promo forms show the correct fields.

## 12. Expected Change Surface

The implementation is expected to touch approximately 18-25 handwritten files,
plus mechanical Ent-generated files.

Primary areas:

- one SQL migration;
- promo-code, promo-code-usage, and payment-order Ent schemas;
- promo domain, service, repository interface, and repository implementation;
- payment request types, order creation, fulfillment, lifecycle, and expiry;
- affiliate rebate integration and audit details;
- WeChat payment OAuth/resume claims and handlers;
- public/admin DTOs and routes;
- user payment types, store/API, checkout view, and resume helpers;
- admin promo-code types/API/view;
- localization strings;
- focused backend and frontend tests.

No unrelated payment refactor or new dependency is required.

## 13. Workload Estimate

Estimated effort for one focused implementation owner:

| Work item | Estimate |
| --- | ---: |
| Migration, Ent schemas, and generated code | 1 hour |
| Promo validation, reservation, consumption, and release | 2-3 hours |
| Payment pricing, expiry/recovery, refund, and affiliate integration | 2-3 hours |
| User and admin frontend changes | 2-3 hours |
| Concurrency, payment lifecycle, and frontend regression tests | 1-2 hours |
| **Total** | **8-12 hours** |

This is approximately 1-1.5 working days for a review-ready, locally verified
change. It excludes production deployment, production database migration,
payment-provider sandbox setup, and real-channel payment testing. Staging or live
payment verification should reserve an additional half day.

## 14. Completion Criteria

The feature is complete when:

1. An administrator can create an active single-use subscription promo code with
   a valid percentage rate.
2. A user can preview and apply it to any subscription purchase or renewal.
3. The backend charges the correct discounted amount and records an immutable
   price snapshot.
4. Only one pending order can reserve the code at a time.
5. Successful payment permanently consumes the code exactly once.
6. An unpaid order releases it after order timeout and the five-minute recovery
   window.
7. Late and duplicate callbacks cannot grant two discounted subscriptions.
8. Refunds use the discounted charged amount and do not restore the code.
9. Affiliate rebate uses the discounted subscription amount, excluding payment
   fees and currency conversion.
10. Existing registration promo codes and non-promo payment flows continue to
    pass their regression tests.
