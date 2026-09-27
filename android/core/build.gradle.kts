plugins { id("com.android.library"); id("org.jetbrains.kotlin.android") }
android { compileOptions { sourceCompatibility=JavaVersion.VERSION_17; targetCompatibility=JavaVersion.VERSION_17 }; namespace="com.redxai.core"; compileSdk=35
    defaultConfig { minSdk=26 }
}
