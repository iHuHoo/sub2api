import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, shallowMount } from '@vue/test-utils'
import PromoCodesView from '../PromoCodesView.vue'

const list = vi.hoisted(() => vi.fn())
const createPromo = vi.hoisted(() => vi.fn())
const updatePromo = vi.hoisted(() => vi.fn())
const getUsages = vi.hoisted(() => vi.fn())

vi.mock('@/api/admin', () => ({
  adminAPI: { promo: {
    list,
    create: createPromo,
    update: updatePromo,
    delete: vi.fn(),
    getUsages,
  } },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

vi.mock('vue-i18n', async (importOriginal) => ({
  ...await importOriginal<typeof import('vue-i18n')>(),
  useI18n: () => ({ t: (key: string, params?: Record<string, unknown>) => `${key}${params?.id ?? ''}` }),
}))

const subscriptionCode = {
  id: 1,
  code: 'SAVE20',
  purpose: 'subscription_discount',
  discount_rate: 0.8,
  bonus_amount: 0,
  max_uses: 1,
  used_count: 0,
  status: 'active',
  expires_at: null,
  notes: null,
  created_at: '2026-08-27T00:00:00Z',
  updated_at: '2026-08-27T00:00:00Z',
}

function mountView() {
  return shallowMount(PromoCodesView, {
    global: { stubs: {
      AppLayout: { template: '<div><slot /></div>' },
      TablePageLayout: { template: '<div><slot name="filters"/><slot name="table"/><slot name="pagination"/></div>' },
      DataTable: {
        props: ['data'],
        template: '<div><slot v-if="data[0]" name="cell-actions" :row="data[0]" /></div>',
      },
      BaseDialog: {
        props: ['show'],
        template: '<div v-if="show"><slot/><slot name="footer"/></div>',
      },
      Select: true,
      Pagination: true,
      ConfirmDialog: true,
      Icon: true,
    } },
  })
}

beforeEach(() => {
  list.mockReset().mockResolvedValue({ items: [subscriptionCode], total: 1 })
  createPromo.mockReset().mockResolvedValue(subscriptionCode)
  updatePromo.mockReset().mockResolvedValue(subscriptionCode)
  getUsages.mockReset()
})

describe('PromoCodesView subscription promos', () => {
  it('creates a single-use subscription discount from payable percent', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.findAll('button').find(button => button.text().includes('admin.promo.createCode'))!.trigger('click')
    await wrapper.get('[data-testid="promo-purpose"]').setValue('subscription_discount')
    await wrapper.get('[data-testid="promo-pay-percent"]').setValue('80')
    await wrapper.get('#create-promo-form').trigger('submit')
    await flushPromises()

    expect(createPromo).toHaveBeenCalledWith(expect.objectContaining({
      purpose: 'subscription_discount',
      discount_rate: 0.8,
      bonus_amount: 0,
      max_uses: 1,
    }))
  })

  it('updates a subscription discount as globally single-use', async () => {
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('button[title="common.edit"]').trigger('click')
    await wrapper.get('[data-testid="promo-edit-pay-percent"]').setValue('90')
    await wrapper.get('#edit-promo-form').trigger('submit')
    await flushPromises()

    expect(updatePromo).toHaveBeenCalledWith(1, expect.objectContaining({
      purpose: 'subscription_discount',
      discount_rate: 0.9,
      bonus_amount: 0,
      max_uses: 1,
    }))
  })

  it.each(['reserved', 'consumed', 'released'])('renders %s subscription usage', async (status) => {
    getUsages.mockResolvedValue({ items: [{
      id: 2,
      promo_code_id: 1,
      user_id: 9,
      payment_order_id: 42,
      usage_type: 'subscription_discount',
      status,
      bonus_amount: 0,
      discount_amount: 29.8,
      used_at: '2026-08-27T00:00:00Z',
      reserved_at: '2026-08-27T00:00:00Z',
    }], total: 1 })
    const wrapper = mountView()
    await flushPromises()
    await wrapper.find('button[title="admin.promo.viewUsages"]').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain(status)
    expect(wrapper.text()).toContain('42')
  })
})
