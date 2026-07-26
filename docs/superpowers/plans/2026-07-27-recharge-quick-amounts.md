# Recharge Quick Amounts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show only `100`, `200`, and `500` as recharge shortcuts, render CNY payment input with `¥`, and call the purchased quantity `Credits` on the recharge selection and waiting pages.

**Architecture:** Keep payment amounts and order calculations unchanged. Add a presentation-only currency-symbol prop to `AmountInput`, derive it from the selected payment method in `PaymentView`, and format credited quantities with a `Credits` suffix only inside `PaymentView` and `PaymentStatusPanel`.

**Tech Stack:** Vue 3, TypeScript, Vue Test Utils, Vitest

## Global Constraints

- Quick and custom amounts remain payment amounts.
- The quick amount list is exactly `[100, 200, 500]`.
- CNY payment input displays `¥`; other payment currencies use their existing symbol.
- Only the recharge selection and waiting-for-payment flow use `Credits` wording.
- Completed account balance, order list, payment result page, backend amounts, limits, subscription prices, and order calculations remain unchanged.
- Do not add an admin setting.

---

### Task 1: Recharge selection amounts and units

**Files:**
- Create: `frontend/src/components/payment/__tests__/AmountInput.spec.ts`
- Modify: `frontend/src/components/payment/AmountInput.vue`
- Modify: `frontend/src/views/user/PaymentView.vue`
- Modify: `frontend/src/views/user/__tests__/PaymentView.spec.ts`

**Interfaces:**
- `AmountInput` consumes a new optional prop `currencySymbol?: string`, defaulting to `$`.
- `PaymentView` passes `currencySymbol(selectedCurrency)` and `[100, 200, 500]` to `AmountInput`.
- `PaymentView` renders credited quantity as `<amount> Credits` while keeping `creditedAmount` calculation unchanged.

- [ ] **Step 1: Write failing component and view tests**

Add a real-component test in `AmountInput.spec.ts`:

```ts
import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import AmountInput from '../AmountInput.vue'

describe('AmountInput payment currency', () => {
  it('shows the configured currency symbol and only the supplied quick amounts', () => {
    const wrapper = mount(AmountInput, {
      props: {
        modelValue: null,
        amounts: [100, 200, 500],
        currencySymbol: '¥',
      },
    })

    expect(wrapper.text()).toContain('¥')
    expect(wrapper.findAll('button').map(button => button.text())).toEqual(['100', '200', '500'])
    expect(wrapper.text()).not.toContain('$')
  })
})
```

Extend `PaymentView.spec.ts` with a CNY recharge test that mounts the existing checkout fixture using:

```ts
checkout: {
  balance_recharge_multiplier: 0.11363636,
},
method: {
  currency: 'CNY',
},
```

Then assert the `AmountInput` stub receives:

```ts
expect(amountInput.props('amounts')).toEqual([100, 200, 500])
expect(amountInput.props('currencySymbol')).toBe('¥')
```

Emit `update:modelValue` with `100` and assert:

```ts
expect(wrapper.text()).toContain('11.36 Credits')
expect(wrapper.text()).not.toContain('$11.36')
```

- [ ] **Step 2: Run the focused tests and verify RED**

Run:

```bash
corepack pnpm@9.15.9 --dir frontend test:run src/components/payment/__tests__/AmountInput.spec.ts src/views/user/__tests__/PaymentView.spec.ts
```

Expected: FAIL because `AmountInput` has no `currencySymbol` prop, `PaymentView` still passes the old quick amount array, and credited quantity still uses `$`.

- [ ] **Step 3: Implement the minimal selection-page changes**

In `AmountInput.vue`, add:

```ts
currencySymbol?: string
```

with default:

```ts
currencySymbol: '$',
```

and replace the hard-coded `$` in the template with:

```vue
{{ currencySymbol }}
```

In `PaymentView.vue`:

```vue
<AmountInput
  v-model="amount"
  :amounts="[100, 200, 500]"
  :currency-symbol="currencySymbol(selectedCurrency)"
  :min="globalMinAmount"
  :max="globalMaxAmount"
/>
```

Import `currencySymbol` from `@/components/payment/currency`, and replace:

```vue
${{ creditedAmount.toFixed(2) }}
```

with:

```vue
{{ creditedAmount.toFixed(2) }} Credits
```

- [ ] **Step 4: Run focused tests and verify GREEN**

Run the same focused Vitest command from Step 2.

Expected: PASS with no warnings or errors.

- [ ] **Step 5: Commit Task 1**

```bash
git add frontend/src/components/payment/AmountInput.vue frontend/src/components/payment/__tests__/AmountInput.spec.ts frontend/src/views/user/PaymentView.vue frontend/src/views/user/__tests__/PaymentView.spec.ts
git commit -m "fix(frontend): clarify recharge payment amounts"
```

### Task 2: Waiting-page Credits display

**Files:**
- Modify: `frontend/src/components/payment/PaymentStatusPanel.vue`
- Modify: `frontend/src/components/payment/__tests__/PaymentStatusPanel.spec.ts`

**Interfaces:**
- `PaymentStatusPanel` continues consuming existing `amount?: number` and `orderType?: string` props.
- For `orderType === 'balance'`, the active waiting state and terminal order amount render `<amount> Credits`.
- Subscription display and pages outside `PaymentStatusPanel` remain unchanged.

- [ ] **Step 1: Write the failing waiting-page tests**

Add one test that mounts a pending balance order with:

```ts
props: {
  orderId: 42,
  amount: 11.36,
  payAmount: 100,
  qrCode: 'https://pay.example.com/qr/42',
  expiresAt: '2099-01-01T12:30:00Z',
  paymentType: 'alipay',
  orderType: 'balance',
  currency: 'CNY',
}
```

and asserts:

```ts
expect(wrapper.text()).toContain('11.36 Credits')
expect(wrapper.text()).not.toContain('$11.36')
```

Extend the existing successful-terminal-state test so the returned balance order has `amount: 11.36`, then assert the terminal panel also contains `11.36 Credits`.

- [ ] **Step 2: Run the component test and verify RED**

Run:

```bash
corepack pnpm@9.15.9 --dir frontend test:run src/components/payment/__tests__/PaymentStatusPanel.spec.ts
```

Expected: FAIL because the active waiting state does not show the purchased quantity and the success state prefixes it with `$`.

- [ ] **Step 3: Implement the minimal waiting-page display**

Add a computed formatter:

```ts
const purchasedCredits = computed(() => {
  if (props.orderType !== 'balance' || !props.amount || props.amount <= 0) return ''
  return `${props.amount.toFixed(2)} Credits`
})
```

Render it once above the active-state branches when `outcome === null`, and make the terminal amount conditional:

```vue
<span class="font-medium text-gray-900 dark:text-white">
  {{ props.orderType === 'balance'
    ? `${paidOrder.amount.toFixed(2)} Credits`
    : `${creditedAmountSymbol}${paidOrder.amount.toFixed(2)}` }}
</span>
```

- [ ] **Step 4: Run the component test and verify GREEN**

Run the same focused Vitest command from Step 2.

Expected: PASS with no warnings or errors.

- [ ] **Step 5: Commit Task 2**

```bash
git add frontend/src/components/payment/PaymentStatusPanel.vue frontend/src/components/payment/__tests__/PaymentStatusPanel.spec.ts
git commit -m "fix(frontend): label checkout quantity as Credits"
```

### Task 3: Regression verification

**Files:**
- No production files

**Interfaces:**
- Consumes the completed Task 1 and Task 2 behavior.
- Produces verification evidence only.

- [ ] **Step 1: Run the complete affected test suites**

```bash
corepack pnpm@9.15.9 --dir frontend test:run src/components/payment/__tests__/AmountInput.spec.ts src/components/payment/__tests__/PaymentStatusPanel.spec.ts src/views/user/__tests__/PaymentView.spec.ts
```

Expected: all tests pass.

- [ ] **Step 2: Run frontend type checking**

```bash
corepack pnpm@9.15.9 --dir frontend typecheck
```

Expected: exit code `0`.

- [ ] **Step 3: Inspect the final diff**

```bash
git diff origin/main...HEAD --check
git diff --stat origin/main...HEAD
git status --short
```

Expected: no whitespace errors; only the design/plan, four production/test areas named above, and the new focused test are changed; worktree is clean after commits.
