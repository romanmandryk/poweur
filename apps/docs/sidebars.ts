import type { SidebarsConfig } from '@docusaurus/plugin-content-docs';

// Ordered by reader: people getting an ID, then operators, then developers,
// then the protocol reference.
const sidebars: SidebarsConfig = {
  docsSidebar: [
    {
      type: 'doc',
      id: 'intro',
      label: 'Introduction',
    },
    {
      type: 'category',
      label: 'Get started',
      collapsed: false,
      items: [
        'web/claim-your-id',
        'web/walkthrough',
      ],
    },
    {
      type: 'category',
      label: 'Run a relay',
      collapsed: false,
      items: [
        'relay/self-hosting',
        'relay/overview',
        'relay/configuration',
        'relay/observability',
        'relay/dns-management',
        'relay/api-reference',
      ],
    },
    {
      type: 'category',
      label: 'Build with Poweur',
      collapsed: false,
      items: [
        'clients/overview',
        'clients/js-sdk',
        'clients/cli-reference',
      ],
    },
    {
      type: 'category',
      label: 'Sign in with Poweur ID',
      collapsed: false,
      items: [
        'auth/sign-in',
        'auth/add-sign-in',
        'auth/connected-apps',
        'auth/mobile-signer',
        'auth/interop-bridges',
        'auth/oauth-oidc-bridge',
      ],
    },
    {
      type: 'category',
      label: 'Protocol',
      collapsed: true,
      items: [
        'protocol/overview',
        'protocol/identity-model',
        'protocol/web-identity',
        'protocol/dns-records',
        'protocol/message-format',
        'protocol/routing',
        'protocol/delivery-acks',
        'protocol/group-messaging',
        'protocol/interoperability',
        'protocol/rate-limiting',
      ],
    },
    {
      type: 'category',
      label: 'Files',
      collapsed: true,
      items: [
        'files/storage-v2',
        'files/storage-v2-adr',
        'files/group-identities',
      ],
    },
    {
      type: 'category',
      label: 'Trust & anti-spam',
      collapsed: true,
      items: [
        'trust/contacts',
        'trust/anonymous-and-challenges',
        'trust/relay-reputation',
      ],
    },
    {
      type: 'category',
      label: 'Security',
      collapsed: true,
      items: [
        'security/model',
        'security/key-management',
        'security/tls',
      ],
    },
    {
      type: 'category',
      label: 'Future',
      collapsed: true,
      items: [
        'future/capabilities',
        'future/mls-adoption',
      ],
    },
  ],
};

export default sidebars;
