plugins { id("com.android.application"); id("org.jetbrains.kotlin.android"); id("org.jetbrains.kotlin.plugin.compose") }
android { compileOptions { sourceCompatibility=JavaVersion.VERSION_17; targetCompatibility=JavaVersion.VERSION_17 }; namespace="com.redxai.database"; compileSdk=35
    defaultConfig { applicationId="com.redxai.database"; minSdk=26; targetSdk=35; versionCode=1; versionName="0.1.0" }
    signingConfigs {
        create("release") {
            val ks = System.getenv("REDXAI_KEYSTORE")
            if (!ks.isNullOrBlank()) {
                storeFile = file(ks)
                storePassword = System.getenv("REDXAI_STORE_PASSWORD")
                keyAlias = System.getenv("REDXAI_KEY_ALIAS")
                keyPassword = System.getenv("REDXAI_KEY_PASSWORD")
            }
        }
    }
    buildTypes { getByName("release") { signingConfig = signingConfigs.getByName("release"); isMinifyEnabled = false } }
    buildFeatures { compose=true }
}
dependencies {
    implementation(project(":core"))
    implementation(platform("androidx.compose:compose-bom:2025.03.01"))
    implementation("androidx.activity:activity-compose:1.10.1")
    implementation("androidx.compose.material3:material3")
}
